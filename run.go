// run.go

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"time"

	gt "github.com/meinside/gemini-things-go"

	infisical "github.com/infisical/go-sdk"
	"github.com/infisical/go-sdk/packages/models"
	"github.com/tailscale/hujson"
)

const (
	appName = "gmn"

	defaultConfigFilename          = "config.json"
	defaultGoogleAIModel           = "gemini-1.5-flash-latest"
	defaultSystemInstructionFormat = `You are a CLI which uses Google Gemini API(model: %[1]s).

Current datetime is %[2]s, and hostname is '%[3]s'.

Respond to user messages according to the following principles:
- Do not repeat the user's request.
- Be as accurate as possible.
- Be as truthful as possible.
- Be as comprehensive and informative as possible.
`

	defaultTimeoutSeconds         = 5 * 60 // 5 minutes
	defaultFetchURLTimeoutSeconds = 10     // 10 seconds
	defaultUserAgent              = `GMN/url2text`
)

type config struct {
	GoogleAIAPIKey *string           `json:"google_ai_api_key,omitempty"`
	Infisical      *infisicalSetting `json:"infisical,omitempty"`

	GoogleAIModel     *string `json:"google_ai_model,omitempty"`
	SystemInstruction *string `json:"system_instruction,omitempty"`

	TimeoutSeconds int `json:"timeout_seconds,omitempty"`

	ReplaceHTTPURLsInPrompt      bool `json:"replace_http_urls_in_prompt,omitempty"`
	ReplaceHTTPURLTimeoutSeconds int  `json:"replace_http_url_timeout_seconds,omitempty"`
}

// infisical setting struct
type infisicalSetting struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`

	ProjectID   string `json:"project_id"`
	Environment string `json:"environment"`
	SecretType  string `json:"secret_type"`

	GoogleAIAPIKeyKeyPath string `json:"google_ai_api_key_key_path"`
}

// standardize given JSON (JWCC) bytes
func standardizeJSON(b []byte) ([]byte, error) {
	ast, err := hujson.Parse(b)
	if err != nil {
		return b, err
	}
	ast.Standardize()

	return ast.Pack(), nil
}

// read config from given filepath
func readConfig(configFilepath string) (conf config, err error) {
	var bytes []byte
	if bytes, err = os.ReadFile(configFilepath); err == nil {
		if bytes, err = standardizeJSON(bytes); err == nil {
			if err = json.Unmarshal(bytes, &conf); err == nil {
				// set default values
				if conf.TimeoutSeconds <= 0 {
					conf.TimeoutSeconds = defaultTimeoutSeconds
				}
				if conf.ReplaceHTTPURLTimeoutSeconds <= 0 {
					conf.ReplaceHTTPURLTimeoutSeconds = defaultFetchURLTimeoutSeconds
				}

				if conf.GoogleAIAPIKey == nil && conf.Infisical != nil {
					// read token and api key from infisical
					client := infisical.NewInfisicalClient(infisical.Config{
						SiteUrl: "https://app.infisical.com",
					})

					_, err = client.Auth().UniversalAuthLogin(conf.Infisical.ClientID, conf.Infisical.ClientSecret)
					if err != nil {
						return config{}, fmt.Errorf("failed to authenticate with Infisical: %s", err)
					}

					var keyPath string
					var secret models.Secret

					// google ai api key
					keyPath = conf.Infisical.GoogleAIAPIKeyKeyPath
					secret, err = client.Secrets().Retrieve(infisical.RetrieveSecretOptions{
						ProjectID:   conf.Infisical.ProjectID,
						Type:        conf.Infisical.SecretType,
						Environment: conf.Infisical.Environment,
						SecretPath:  path.Dir(keyPath),
						SecretKey:   path.Base(keyPath),
					})
					if err == nil {
						val := secret.SecretValue
						conf.GoogleAIAPIKey = &val
					} else {
						return config{}, fmt.Errorf("failed to retrieve `google_ai_api_key` from Infisical: %s", err)
					}
				}

				return conf, nil
			}
		}
	}

	return conf, err
}

// resolve config filepath
func resolveConfigFilepath(configFilepath *string) string {
	if configFilepath != nil {
		return *configFilepath
	}

	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome != "" {
		return filepath.Join(configHome, appName, defaultConfigFilename)
	}

	return filepath.Join(os.Getenv("HOME"), ".config", appName, defaultConfigFilename)
}

// run with params
func run(p params) {
	var err error
	var conf config

	// read and apply configs
	if conf, err = readConfig(resolveConfigFilepath(p.ConfigFilepath)); err == nil {
		if p.SystemInstruction == nil && conf.SystemInstruction != nil {
			p.SystemInstruction = conf.SystemInstruction
		}
	} else {
		logAndExit(1, "Failed to read configuration: %s", err)
	}

	// override parameters with command arguments
	if conf.GoogleAIAPIKey != nil && p.GoogleAIAPIKey == nil {
		p.GoogleAIAPIKey = conf.GoogleAIAPIKey
	}
	if conf.GoogleAIModel != nil && p.GoogleAIModel == nil {
		p.GoogleAIModel = conf.GoogleAIModel
	}

	// set default values
	if p.GoogleAIModel == nil {
		p.GoogleAIModel = ptr(defaultGoogleAIModel)
	}
	if p.SystemInstruction == nil {
		p.SystemInstruction = ptr(defaultSystemInstruction(conf))
	}
	if p.UserAgent == nil {
		p.UserAgent = ptr(defaultUserAgent)
	}

	// check existence of essential parameters here
	if conf.GoogleAIAPIKey == nil {
		logAndExit(1, "Google AI API Key is missing")
	}

	// replace urls in the prompt
	promptFiles := map[string][]byte{}
	if conf.ReplaceHTTPURLsInPrompt {
		p.Prompt, promptFiles = replaceURLsInPrompt(conf, p)

		if checkVerbosity(p.Verbose) >= verboseMedium {
			verbose(p.Verbose, "replaced prompt: %s\n\n", p.Prompt)
		}
	}

	if checkVerbosity(p.Verbose) >= verboseMaximum {
		verbose(p.Verbose, "requesting with parameters: %s\n\n", prettify(p))
	}

	// do the actual job
	doGeneration(context.TODO(), conf.TimeoutSeconds, *p.GoogleAIAPIKey, *p.GoogleAIModel, *p.SystemInstruction, p.Prompt, promptFiles, p.Filepaths, p.Verbose)
}

// generate with given things
func doGeneration(ctx context.Context, timeoutSeconds int, googleAIAPIKey, googleAIModel, systemInstruction, prompt string, promptFiles map[string][]byte, filepaths []*string, vb []bool) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	// gemini things client
	gtc := gt.NewClient(googleAIModel, googleAIAPIKey)
	gtc.SetTimeout(timeoutSeconds)
	gtc.SetSystemInstructionFunc(func() string {
		return systemInstruction
	})

	// read & close files
	files := []io.Reader{}
	filesToClose := []*os.File{}
	for _, file := range promptFiles {
		files = append(files, bytes.NewReader(file))
	}
	for _, fp := range filepaths {
		if opened, err := os.Open(*fp); err == nil {
			files = append(files, opened)
			filesToClose = append(filesToClose, opened)
		} else {
			logAndExit(1, "Failed to open file: %s", err)
		}
	}
	defer func() {
		for _, toClose := range filesToClose {
			if err := toClose.Close(); err != nil {
				errr("Failed to close file: %s", err)
			}
		}
	}()

	// generate
	if err := gtc.GenerateStreamed(
		ctx,
		prompt,
		files,
		func(data gt.StreamCallbackData) {
			if data.TextDelta != nil {
				fmt.Print(*data.TextDelta)
			} else if data.NumTokens != nil {
				fmt.Print("\n") // FIXME: append a new line to the end of generated output

				// print the number of tokens
				if checkVerbosity(vb) >= verboseMinimum {
					verbose(vb, "input tokens: %d / output tokens: %d", data.NumTokens.Input, data.NumTokens.Output)
				}
			} else if data.Error != nil {
				logAndExit(1, "Streaming failed: %s", data.Error)
			}
		},
		nil,
	); err != nil {
		logAndExit(1, "Generation failed: %s", err)
	}
}

// generate a default system instruction with given configuration
func defaultSystemInstruction(conf config) string {
	datetime := time.Now().Format("2006-01-02 15:04:05 MST (Mon)")
	hostname, _ := os.Hostname()

	return fmt.Sprintf(defaultSystemInstructionFormat,
		*conf.GoogleAIModel,
		datetime,
		hostname,
	)
}
