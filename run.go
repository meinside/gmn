// run.go

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/jessevdk/go-flags"
	"google.golang.org/genai"

	gt "github.com/meinside/gemini-things-go"
	"github.com/meinside/version-go"
)

// run with params
func run(
	parser *flags.Parser,
	p params,
) (exit int, err error) {
	// early return if no task was requested
	if !p.taskRequested() {
		logMessage(
			verboseMedium,
			"No task was requested.\n\n",
		)

		return printHelpBeforeExit(1, parser), nil
	}

	// early return after printing the version
	if p.ShowVersion {
		logMessage(
			verboseMinimum,
			"%s %s\n\n",
			appName,
			version.Build(version.OS|version.Architecture),
		)

		return printHelpBeforeExit(0, parser), nil
	}

	// read and apply configs
	var conf config
	if conf, err = readConfig(resolveConfigFilepath(p.Configuration.ConfigFilepath)); err == nil {
		if p.Generation.SystemInstruction == nil && conf.SystemInstruction != nil {
			p.Generation.SystemInstruction = conf.SystemInstruction
		}
	} else {
		return 1, fmt.Errorf(
			"failed to read configuration: %w",
			err,
		)
	}

	// override command arguments with values from configs
	if conf.GoogleAIAPIKey != nil && p.Configuration.GoogleAIAPIKey == nil {
		p.Configuration.GoogleAIAPIKey = conf.GoogleAIAPIKey
	}

	// set default values
	if p.Generation.SystemInstruction == nil {
		p.Generation.SystemInstruction = ptr(defaultSystemInstruction())
	}
	if p.Generation.UserAgent == nil {
		p.Generation.UserAgent = ptr(defaultUserAgent)
	}

	// check existence of essential parameters here
	if conf.GoogleAIAPIKey == nil {
		return 1, fmt.Errorf("google AI API Key is missing")
	}

	// expand filepaths (recurse directories)
	p.Generation.Filepaths, err = expandFilepaths(p)
	if err != nil {
		return 1, fmt.Errorf(
			"failed to read given filepaths: %w",
			err,
		)
	}

	if p.hasPrompt() { // if prompt is given,
		logVerbose(
			verboseMaximum,
			p.Verbose,
			"request params with prompt: %s\n\n",
			prettify(p.redact()),
		)

		if p.Embeddings.GenerateEmbeddings { // generate embeddings with given prompt,
			// model
			if p.Configuration.GoogleAIModel == nil {
				if conf.GoogleAIEmbeddingsModel != nil {
					p.Configuration.GoogleAIModel = conf.GoogleAIEmbeddingsModel
				} else {
					p.Configuration.GoogleAIModel = ptr(defaultGoogleAIEmbeddingsModel)
				}
			}

			return doEmbeddingsGeneration(context.TODO(),
				conf.TimeoutSeconds,
				*p.Configuration.GoogleAIAPIKey,
				*p.Configuration.GoogleAIModel,
				*p.Generation.Prompt,
				p.Embeddings.EmbeddingsTaskType,
				p.Embeddings.EmbeddingsChunkSize,
				p.Embeddings.EmbeddingsOverlappedChunkSize,
				p.Verbose,
			)
		} else {
			prompts := []gt.Prompt{}
			promptFiles := map[string][]byte{}

			if p.Generation.ReplaceHTTPURLsInPrompt {
				// replace urls in the prompt,
				replacedPrompt, extractedFiles := replaceURLsInPrompt(conf, p)

				prompts = append(prompts, gt.PromptFromText(replacedPrompt))

				for customURL, data := range extractedFiles {
					if customURL.isLink() {
						promptFiles[customURL.url()] = data
					} else if customURL.isYoutube() {
						prompts = append(prompts, gt.PromptFromURI(customURL.url()))
					}
				}

				logVerbose(
					verboseMedium,
					p.Verbose,
					"replaced prompt: %s\n\nresulting prompts: %v\n\n",
					replacedPrompt,
					prompts,
				)
			} else {
				// or, use the given prompt as it is,
				prompts = append(prompts, gt.PromptFromText(*p.Generation.Prompt))
			}

			if p.Caching.CacheContext { // cache context
				// model
				if p.Configuration.GoogleAIModel == nil {
					if conf.GoogleAIModel != nil {
						p.Configuration.GoogleAIModel = conf.GoogleAIModel
					} else {
						p.Configuration.GoogleAIModel = ptr(defaultGoogleAIModel)
					}
				}

				return cacheContext(context.TODO(),
					conf.TimeoutSeconds,
					*p.Configuration.GoogleAIAPIKey,
					*p.Configuration.GoogleAIModel,
					*p.Generation.SystemInstruction,
					prompts,
					promptFiles,
					p.Generation.Filepaths,
					p.Caching.CachedContextName,
					p.Verbose,
				)
			} else { // generate
				// model
				if p.Generation.GenerateImages {
					if p.Configuration.GoogleAIModel == nil {
						if conf.GoogleAIImageGenerationModel != nil {
							p.Configuration.GoogleAIModel = conf.GoogleAIImageGenerationModel
						} else {
							p.Configuration.GoogleAIModel = ptr(defaultGoogleAIImageGenerationModel)
						}
					}
				} else if p.Generation.GenerateSpeech {
					if p.Configuration.GoogleAIModel == nil {
						if conf.GoogleAISpeechGenerationModel != nil {
							p.Configuration.GoogleAIModel = conf.GoogleAISpeechGenerationModel
						} else {
							p.Configuration.GoogleAIModel = ptr(defaultGoogleAISpeechGenerationModel)
						}
					}
				} else {
					if p.Configuration.GoogleAIModel == nil {
						if conf.GoogleAIModel != nil {
							p.Configuration.GoogleAIModel = conf.GoogleAIModel
						} else {
							p.Configuration.GoogleAIModel = ptr(defaultGoogleAIModel)
						}
					}
				}

				// function call
				var tools []genai.Tool
				if p.Generation.Tools != nil {
					if bytes, err := standardizeJSON([]byte(*p.Generation.Tools)); err == nil {
						if err := json.Unmarshal(bytes, &tools); err != nil {
							return 1, fmt.Errorf(
								"failed to read tools: %w",
								err,
							)
						}
					} else {
						return 1, fmt.Errorf(
							"failed to standardize tools' JSON: %w",
							err,
						)
					}
				}
				var toolConfig *genai.ToolConfig
				if p.Generation.ToolConfig != nil {
					if bytes, err := standardizeJSON([]byte(*p.Generation.ToolConfig)); err == nil {
						if err := json.Unmarshal(bytes, &toolConfig); err != nil {
							return 1, fmt.Errorf(
								"failed to read tool config: %w",
								err,
							)
						}
					} else {
						return 1, fmt.Errorf(
							"failed to standardize tool config's JSON: %w",
							err,
						)
					}
				}
				// NOTE: both `tools` and `toolConfig` should be given at the same time
				if tools != nil && toolConfig == nil ||
					tools == nil && toolConfig != nil {
					return 1, fmt.Errorf("both tools and tool config should be given at the same time")
				}

				return doGeneration(context.TODO(),
					conf.TimeoutSeconds,
					*p.Configuration.GoogleAIAPIKey,
					*p.Configuration.GoogleAIModel,
					*p.Generation.SystemInstruction,
					p.Generation.Temperature,
					p.Generation.TopP,
					p.Generation.TopK,
					prompts,
					promptFiles,
					p.Generation.Filepaths,
					p.Generation.ThinkingOn,
					p.Generation.ThinkingBudget,
					p.Generation.GroundingOn,
					p.Caching.CachedContextName,
					tools,
					toolConfig,
					p.Generation.ToolCallbacks,
					p.Generation.ToolCallbacksConfirm,
					p.Generation.RecurseOnCallbackResults,
					p.Generation.OutputAsJSON,
					p.Generation.GenerateImages,
					p.Generation.SaveImagesToFiles,
					p.Generation.SaveImagesToDir,
					p.Generation.GenerateSpeech,
					p.Generation.SpeechLanguage,
					p.Generation.SpeechVoice,
					p.Generation.SpeechVoices,
					p.Generation.SaveSpeechToDir,
					nil, // NOTE: first call => no history
					!p.ErrorOnUnsupportedType,
					p.Verbose,
				)
			}
		}
	} else { // if prompt is not given,
		logVerbose(
			verboseMaximum,
			p.Verbose,
			"request params without prompt: %s\n\n",
			prettify(p.redact()),
		)

		if p.Caching.CacheContext { // cache context
			return cacheContext(context.TODO(),
				conf.TimeoutSeconds,
				*p.Configuration.GoogleAIAPIKey,
				*p.Configuration.GoogleAIModel,
				*p.Generation.SystemInstruction,
				nil, // prompt not given
				nil, // prompt not given
				p.Generation.Filepaths,
				p.Caching.CachedContextName,
				p.Verbose,
			)
		} else if p.Caching.ListCachedContexts { // list cached contexts
			return listCachedContexts(context.TODO(),
				conf.TimeoutSeconds,
				*p.Configuration.GoogleAIAPIKey,
				p.Verbose,
			)
		} else if p.Caching.DeleteCachedContext != nil { // delete cached context
			return deleteCachedContext(context.TODO(),
				conf.TimeoutSeconds,
				*p.Configuration.GoogleAIAPIKey,
				*p.Caching.DeleteCachedContext,
				p.Verbose,
			)
		} else if p.ListModels { // list models
			return listModels(context.TODO(),
				conf.TimeoutSeconds,
				*p.Configuration.GoogleAIAPIKey,
				p.Verbose,
			)
		} else { // otherwise, (should not reach here)
			logMessage(
				verboseMedium,
				"Parameter error: no task was requested or handled properly.",
			)

			return printHelpBeforeExit(1, parser), nil
		}
	}
}

// generate a default system instruction with given params
func defaultSystemInstruction() string {
	datetime := time.Now().Format("2006-01-02 15:04:05 MST (Mon)")
	hostname, _ := os.Hostname()

	return fmt.Sprintf(defaultSystemInstructionFormat,
		appName,
		datetime,
		hostname,
	)
}
