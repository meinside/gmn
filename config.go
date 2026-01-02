// config.go
//
// Things for configurations of this application.

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
)

const (
	// environment variable names
	envVarNameAPIKey              = `GEMINI_API_KEY`
	envVarNameCredentialsFilepath = `CREDENTIALS_FILEPATH`

	// default config file's name
	defaultConfigFilename       = `config.json`
	defaultConfigTimeoutSeconds = 10

	// default model names
	defaultGoogleAIModel                 = `gemini-3-flash-preview`
	defaultGoogleAIImageGenerationModel  = `gemini-3-pro-image-preview`
	defaultGoogleAIVideoGenerationModel  = `veo-3.1-fast-generate-preview`
	defaultGoogleAISpeechGenerationModel = `gemini-2.5-pro-preview-tts`
	defaultGoogleAIEmbeddingsModel       = `gemini-embedding-001`

	// default system instruction
	defaultSystemInstructionFormat = `You are a CLI named '%[1]s' which uses Google Gemini API.

Current datetime is %[2]s, and hostname is '%[3]s'.

Respond to user messages according to the following principles:
- Do not repeat the user's request and return only the response to the user's request.
- Unless otherwise specified, respond in the same language as used in the user's request.
- Be as accurate as possible.
- Be as truthful as possible.
- Be as comprehensive and informative as possible.
- If you are generating with tool calling, make sure not to repeat the same response by checking the previous conversations and history.
- Try not to call the same tool with the same arguments repeatedly if the result of tool call was not erroneous.
`

	// default values for generation
	defaultNumGeneratedVideos             = 1
	defaultGeneratedVideosDurationSeconds = 8
	defaultGeneratedVideosFPS             = 24

	// other default parameters
	defaultTimeoutSeconds           = 5 * 60 // 5 minutes
	defaultFetchURLTimeoutSeconds   = 10     // 10 seconds
	defaultFetchUserAgent           = `gmn/fetcher`
	defaultLocation                 = `global`
	defaultBucketNameForFileUploads = `gmn-file-uploads`
)

// config struct
type config struct {
	// (1) gemini api key in plain text
	GoogleAIAPIKey *string `json:"google_ai_api_key,omitempty"`

	// (2) or, gemini api key in infisical
	Infisical *infisicalSetting `json:"infisical,omitempty"`

	// (3) or, google credentials file path
	GoogleCredentialsFilepath                  *string `json:"google_credentials_filepath,omitempty"`
	Location                                   *string `json:"location,omitempty"`
	GoogleCloudStorageBucketNameForFileUploads *string `json:"gcs_bucket_name_for_file_uploads,omitempty"`

	GoogleAIModel                 *string `json:"google_ai_model,omitempty"`
	GoogleAIImageGenerationModel  *string `json:"google_ai_image_generation_model,omitempty"`
	GoogleAIVideoGenerationModel  *string `json:"google_ai_video_generation_model,omitempty"`
	GoogleAISpeechGenerationModel *string `json:"google_ai_speech_generation_model,omitempty"`
	GoogleAIEmbeddingsModel       *string `json:"google_ai_embeddings_model,omitempty"`
	SystemInstruction             *string `json:"system_instruction,omitempty"`

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

// read config from given filepath
func readConfig(configFilepath string) (conf config, err error) {
	var bytes []byte

	bytes, err = os.ReadFile(configFilepath)
	if err == nil {
		bytes, err = standardizeJSON(bytes)
		if err == nil {
			err = json.Unmarshal(bytes, &conf)
			if err == nil {
				// set default values
				if conf.TimeoutSeconds <= 0 {
					conf.TimeoutSeconds = defaultTimeoutSeconds
				}
				if conf.ReplaceHTTPURLTimeoutSeconds <= 0 {
					conf.ReplaceHTTPURLTimeoutSeconds = defaultFetchURLTimeoutSeconds
				}

				if conf.GoogleAIAPIKey == nil && conf.Infisical != nil {
					ctx, cancel := context.WithTimeout(context.Background(), defaultConfigTimeoutSeconds*time.Second)
					defer cancel()

					// read token and api key from infisical
					conf, err = fetchConfFromInfisical(ctx, conf)
					if err != nil {
						return config{}, fmt.Errorf("failed to fetch config from Infisical: %w", err)
					}
				}

				if conf.Location == nil {
					conf.Location = ptr(defaultLocation)
				}
				if conf.GoogleCloudStorageBucketNameForFileUploads == nil {
					conf.GoogleCloudStorageBucketNameForFileUploads = ptr(string(defaultBucketNameForFileUploads))
				}
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

// fetch config values from infisical
func fetchConfFromInfisical(
	ctx context.Context,
	conf config,
) (config, error) {
	// read token and api key from infisical
	client := infisical.NewInfisicalClient(
		ctx,
		infisical.Config{
			SiteUrl: "https://app.infisical.com",
		},
	)

	_, err := client.Auth().UniversalAuthLogin(
		conf.Infisical.ClientID,
		conf.Infisical.ClientSecret,
	)
	if err != nil {
		return config{}, err
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
	if err != nil {
		return config{}, err
	}
	conf.GoogleAIAPIKey = ptr(secret.SecretValue)

	return conf, nil
}
