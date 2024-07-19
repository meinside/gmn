// run.go

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/gabriel-vasile/mimetype"
	"github.com/google/generative-ai-go/genai"
	infisical "github.com/infisical/go-sdk"
	"github.com/infisical/go-sdk/packages/models"
	"github.com/tailscale/hujson"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

const (
	appName = "gmn"

	defaultConfigFilename          = "config.json"
	defaultGoogleAIModel           = "gemini-1.5-flash-latest"
	defaultSystemInstructionFormat = `You are a chat bot which is built with Golang and Google Gemini API(model: %[1]s).

Current datetime is %[2]s, and hostname is '%[3]s'.

Respond to user messages according to the following principles:
- Do not repeat the user's request.
- Be as accurate as possible.
- Be as truthful as possible.
- Be as comprehensive and informative as possible.
- Be as concise and meaningful as possible.
- Your response must be in plain text, so do not try to emphasize words with markdown characters.
`

	timeoutSeconds                             = 180 // 3 minutes
	fetchURLTimeoutSeconds                     = 10  // 10 seconds
	uploadedFileStateCheckIntervalMilliseconds = 300 // 300 milliseconds
)

type role string

const (
	roleModel role = "model"
	roleUser  role = "user"
)

type config struct {
	GoogleAIAPIKey *string           `json:"google_ai_api_key,omitempty"`
	Infisical      *infisicalSetting `json:"infisical,omitempty"`

	GoogleAIModel     *string `json:"google_ai_model,omitempty"`
	SystemInstruction *string `json:"system_instruction,omitempty"`

	ReplaceHTTPURLsInPrompt bool `json:"replace_http_urls_in_prompt,omitempty"`
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
		log("Failed to read configuration: %s", err)
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

	// check existence of essential parameters here
	if conf.GoogleAIAPIKey == nil {
		logAndExit(1, "Google AI API Key is missing")
	}

	// replace urls in the prompt
	if conf.ReplaceHTTPURLsInPrompt {
		p.Prompt = replaceHTTPURLsInPromptToBodyTexts(p.Prompt, p.Verbose)

		if p.Verbose {
			log("[verbose] replaced prompt: %s", p.Prompt)
		}
	}

	// do the actual job
	if p.Verbose {
		log("[verbose] parameters: %s", prettify(p))
	}
	doGeneration(context.TODO(), *p.GoogleAIAPIKey, *p.GoogleAIModel, *p.SystemInstruction, p.Prompt, p.Filepath, p.OmitTokenCounts)
}

// generate with given things
func doGeneration(ctx context.Context, googleAIAPIKey, googleAIModel, systemInstruction, prompt string, filepath *string, omitTokenCounts bool) {
	ctx, cancel := context.WithTimeout(ctx, timeoutSeconds*time.Second)
	defer cancel()

	client, err := genai.NewClient(ctx, option.WithAPIKey(googleAIAPIKey))
	if err != nil {
		logAndExit(1, "Failed to create API client: %s", err)
	}
	defer client.Close()

	model := client.GenerativeModel(googleAIModel)

	// system instruction
	model.SystemInstruction = &genai.Content{
		Role: string(roleModel),
		Parts: []genai.Part{
			genai.Text(systemInstruction),
		},
	}

	// safety filters (block only high)
	model.SafetySettings = safetySettings(genai.HarmBlockThreshold(genai.HarmBlockOnlyHigh))

	// prompt (text)
	parts := []genai.Part{
		genai.Text(prompt),
	}

	// prompt (file)
	fileNames := []string{}
	if filepath != nil {
		if file, err := os.Open(*filepath); err == nil {
			if mime, err := mimetype.DetectReader(file); err == nil {
				mimeType := stripCharsetFromMimeType(mime.String())

				if supportedFileMimeType(mimeType) {
					if _, err := file.Seek(0, io.SeekStart); err == nil {
						if file, err := client.UploadFile(ctx, "", file, &genai.UploadFileOptions{
							MIMEType: mimeType,
						}); err == nil {
							parts = append(parts, genai.FileData{
								MIMEType: file.MIMEType,
								URI:      file.URI,
							})

							fileNames = append(fileNames, file.Name) // FIXME: will wait synchronously for it to become active
						} else {
							logAndExit(1, "Failed to upload file %s for prompt: %s", *filepath, err)
						}
					} else {
						logAndExit(1, "Failed to seek to start of file: %s", *filepath)
					}
				} else {
					logAndExit(1, "File type (%s) not suuported: %s", mimeType, *filepath)
				}
			} else {
				logAndExit(1, "Failed to detect MIME type of %s: %s", *filepath, err)
			}
		} else {
			logAndExit(1, "Failed to open file %s: %s", *filepath, err)
		}
	}

	// FIXME: wait for all files to become active
	waitForFiles(ctx, client, fileNames)

	// number of tokens for logging
	var numTokensInput int32 = 0
	var numTokensOutput int32 = 0

	// generate and stream response
	iter := model.GenerateContentStream(ctx, parts...)
	for {
		if it, err := iter.Next(); err == nil {
			var candidate *genai.Candidate
			var content *genai.Content
			var parts []genai.Part

			if len(it.Candidates) > 0 {
				// update number of tokens
				numTokensInput = it.UsageMetadata.PromptTokenCount
				numTokensOutput = it.UsageMetadata.TotalTokenCount - it.UsageMetadata.PromptTokenCount

				candidate = it.Candidates[0]
				content = candidate.Content

				if len(content.Parts) > 0 {
					parts = content.Parts
				}
			}

			for _, part := range parts {
				if text, ok := part.(genai.Text); ok { // (text)
					fmt.Print(string(text))
				} else {
					log("# Unsupported type of part for streaming: %s", prettify(part))
				}
			}
		} else {
			if err != iterator.Done {
				log("# Failed to iterate stream: %s", errorString(err))
			}
			break
		}
	}

	// print the number of tokens
	if !omitTokenCounts {
		log("\n> input tokens: %d / output tokens: %d", numTokensInput, numTokensOutput)
	}
}

// generate safety settings for all supported harm categories
func safetySettings(threshold genai.HarmBlockThreshold) (settings []*genai.SafetySetting) {
	for _, category := range []genai.HarmCategory{
		/*
			// categories for PaLM 2 (Legacy) models
			genai.HarmCategoryUnspecified,
			genai.HarmCategoryDerogatory,
			genai.HarmCategoryToxicity,
			genai.HarmCategoryViolence,
			genai.HarmCategorySexual,
			genai.HarmCategoryMedical,
			genai.HarmCategoryDangerous,
		*/

		// all categories supported by Gemini models
		genai.HarmCategoryHarassment,
		genai.HarmCategoryHateSpeech,
		genai.HarmCategorySexuallyExplicit,
		genai.HarmCategoryDangerousContent,
	} {
		settings = append(settings, &genai.SafetySetting{
			Category:  category,
			Threshold: threshold,
		})
	}

	return settings
}

// wait for all files to be active
func waitForFiles(ctx context.Context, client *genai.Client, fileNames []string) {
	var wg sync.WaitGroup
	for _, fileName := range fileNames {
		wg.Add(1)

		go func(name string) {
			for {
				if file, err := client.GetFile(ctx, name); err == nil {
					if file.State == genai.FileStateActive {
						wg.Done()
						break
					} else {
						time.Sleep(uploadedFileStateCheckIntervalMilliseconds * time.Millisecond)
					}
				} else {
					time.Sleep(uploadedFileStateCheckIntervalMilliseconds * time.Millisecond)
				}
			}
		}(fileName)
	}
	wg.Wait()
}

// generate a default system instruction with given configuration
func defaultSystemInstruction(conf config) string {
	datetime := time.Now().Format("2006-01-02 15:04:05 (Mon)")
	hostname, _ := os.Hostname()

	return fmt.Sprintf(defaultSystemInstructionFormat,
		*conf.GoogleAIModel,
		datetime,
		hostname,
	)
}
