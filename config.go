// config.go
//
// things for configurations

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"

	infisical "github.com/infisical/go-sdk"
	"github.com/infisical/go-sdk/packages/models"
)

// config struct
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
