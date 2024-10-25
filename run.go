// run.go

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"time"

	infisical "github.com/infisical/go-sdk"
	"github.com/infisical/go-sdk/packages/models"
	"github.com/jessevdk/go-flags"
	"github.com/tailscale/hujson"
)

const (
	defaultConfigFilename          = "config.json"
	defaultGoogleAIModel           = "gemini-1.5-flash-002"
	defaultSystemInstructionFormat = `You are a CLI named '%[1]s' which uses Google Gemini API(model: %[2]s).

Current datetime is %[3]s, and hostname is '%[4]s'.

Respond to user messages according to the following principles:
- Do not repeat the user's request and return only the response to the user's request.
- Unless otherwise specified, respond in the same language as used in the user's request.
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

	ReplaceHTTPURLTimeoutSeconds int `json:"replace_http_url_timeout_seconds,omitempty"`
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
					client := infisical.NewInfisicalClient(context.TODO(), infisical.Config{
						SiteUrl: "https://app.infisical.com",
					})

					_, err = client.Auth().UniversalAuthLogin(conf.Infisical.ClientID, conf.Infisical.ClientSecret)
					if err != nil {
						return config{}, fmt.Errorf("failed to authenticate with Infisical: %w", err)
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
						return config{}, fmt.Errorf("failed to retrieve `google_ai_api_key` from Infisical: %w", err)
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

// run with params (will `os.Exit(0)` on success, or `os.Exit(1)` on any error)
func run(parser *flags.Parser, p params) {
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

	if p.hasPrompt() { // if prompt is given,
		// replace urls in the prompt
		replacedPrompt := *p.Prompt
		promptFiles := map[string][]byte{}
		if p.ReplaceHTTPURLsInPrompt {
			replacedPrompt, promptFiles = replaceURLsInPrompt(conf, p)
			p.Prompt = &replacedPrompt

			logVerbose(verboseMedium, p.Verbose, "replaced prompt: %s\n\n", replacedPrompt)
		}

		logVerbose(verboseMaximum, p.Verbose, "requesting with parameters: %s\n\n", prettify(p.redact()))

		if p.CacheContext { // cache context
			// cache context
			cacheContext(context.TODO(),
				conf.TimeoutSeconds,
				*p.GoogleAIAPIKey,
				*p.GoogleAIModel,
				*p.SystemInstruction,
				p.Prompt,
				promptFiles,
				p.Filepaths,
				p.CachedContextName,
				p.Verbose)
		} else { // generate
			doGeneration(context.TODO(),
				conf.TimeoutSeconds,
				*p.GoogleAIAPIKey,
				*p.GoogleAIModel,
				*p.SystemInstruction,
				*p.Prompt,
				promptFiles,
				p.Filepaths,
				p.CachedContextName,
				p.Verbose)
		}
	} else { // if prompt is not given
		if p.CacheContext { // cache context
			cacheContext(context.TODO(),
				conf.TimeoutSeconds,
				*p.GoogleAIAPIKey,
				*p.GoogleAIModel,
				*p.SystemInstruction,
				nil, // prompt not given
				nil, // prompt not given
				p.Filepaths,
				p.CachedContextName,
				p.Verbose)
		} else if p.ListCachedContexts { // list cached contexts
			listCachedContexts(context.TODO(),
				conf.TimeoutSeconds,
				*p.GoogleAIAPIKey,
				*p.GoogleAIModel,
				p.Verbose)
		} else if p.DeleteCachedContext != nil { // delete cached context
			deleteCachedContext(context.TODO(),
				conf.TimeoutSeconds,
				*p.GoogleAIAPIKey,
				*p.GoogleAIModel,
				*p.DeleteCachedContext,
				p.Verbose)
		} else { // otherwise,
			logMessage(verboseMedium, "Parameter `prompt` is missing for your requested task.")

			printHelpAndExit(1, parser)
		}
	}
}

// redact params for printing to stdout
func (p *params) redact() params {
	copied := *p
	copied.GoogleAIAPIKey = ptr("REDACTED")
	return copied
}

// generate a default system instruction with given configuration
func defaultSystemInstruction(conf config) string {
	datetime := time.Now().Format("2006-01-02 15:04:05 MST (Mon)")
	hostname, _ := os.Hostname()

	return fmt.Sprintf(defaultSystemInstructionFormat,
		appName,
		*conf.GoogleAIModel,
		datetime,
		hostname,
	)
}
