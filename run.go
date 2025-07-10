// run.go

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/jessevdk/go-flags"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/genai"

	gt "github.com/meinside/gemini-things-go"
	"github.com/meinside/smithery-go"
	"github.com/meinside/version-go"
)

// run with params
func run(
	parser *flags.Parser,
	p params,
	writer *outputWriter,
) (exit int, err error) {
	// early return if no task was requested
	if !p.taskRequested() {
		writer.print(
			verboseMedium,
			"No task was requested.\n\n",
		)

		return writer.printHelpBeforeExit(1, parser), nil
	}

	// early return after printing the version
	if p.ShowVersion {
		writer.print(
			verboseMinimum,
			"%s %s\n\n",
			appName,
			version.Build(version.OS|version.Architecture),
		)

		return writer.printHelpBeforeExit(0, parser), nil
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
	p.Generation.Filepaths, err = expandFilepaths(writer, p)
	if err != nil {
		return 1, fmt.Errorf(
			"failed to read given filepaths: %w",
			err,
		)
	}

	if p.hasPrompt() { // if prompt is given,
		writer.verbose(
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
				writer,
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
				replacedPrompt, extractedFiles := replaceURLsInPrompt(writer, conf, p)

				prompts = append(prompts, gt.PromptFromText(replacedPrompt))

				for customURL, data := range extractedFiles {
					if customURL.isLink() {
						promptFiles[customURL.url()] = data
					} else if customURL.isYoutube() {
						prompts = append(prompts, gt.PromptFromURI(customURL.url()))
					}
				}

				writer.verbose(
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
					writer,
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

				// function call (local)
				var tools []genai.Tool
				if p.LocalTools.Tools != nil {
					if bytes, err := standardizeJSON([]byte(*p.LocalTools.Tools)); err == nil {
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
				if p.LocalTools.ToolConfig != nil {
					if bytes, err := standardizeJSON([]byte(*p.LocalTools.ToolConfig)); err == nil {
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

				// function call (smithery)
				var sc *smithery.Client
				var allSmitheryTools map[string][]*mcp.Tool = nil
				if conf.SmitheryAPIKey != nil &&
					p.SmitheryTools.SmitheryProfileID != nil &&
					len(p.SmitheryTools.SmitheryServerNames) > 0 {
					sc = smitheryClient(*conf.SmitheryAPIKey)

					for _, smitheryServerName := range p.SmitheryTools.SmitheryServerNames {
						writer.verbose(
							verboseMedium,
							p.Verbose,
							"fetching tools for '%s' from smithery...",
							smitheryServerName,
						)

						var fetchedSmitheryTools []*mcp.Tool
						if fetchedSmitheryTools, err = fetchSmitheryTools(
							context.TODO(),
							sc,
							*p.SmitheryTools.SmitheryProfileID,
							smitheryServerName,
						); err == nil {
							if allSmitheryTools == nil {
								allSmitheryTools = map[string][]*mcp.Tool{}
							}
							allSmitheryTools[smitheryServerName] = fetchedSmitheryTools

							// check if there is any duplicated name of function
							if value, duplicated := duplicated(
								keysFromTools(tools, allSmitheryTools),
							); duplicated {
								return 1, fmt.Errorf(
									"duplicated function name in tools: '%s'",
									value,
								)
							}
						} else {
							return 1, fmt.Errorf(
								"failed to fetch tools from smithery: %w",
								err,
							)
						}
					}
				} else if p.SmitheryTools.SmitheryProfileID != nil || len(p.SmitheryTools.SmitheryServerNames) > 0 {
					if conf.SmitheryAPIKey == nil {
						writer.warn(
							"Smithery API key is not set in the config file, so ignoring it for now.",
						)
					} else {
						writer.warn(
							"Both profile id and server name is needed for using Smithery, so ignoring them for now.",
						)
					}
				}

				return doGeneration(context.TODO(),
					writer,
					conf.TimeoutSeconds,
					*p.Configuration.GoogleAIAPIKey,
					*p.Configuration.GoogleAIModel,
					conf.SmitheryAPIKey,
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
					p.Tools.ShowCallbackResults,
					p.Tools.RecurseOnCallbackResults,
					p.Tools.ForceCallDestructiveTools,
					tools,
					toolConfig,
					p.LocalTools.ToolCallbacks,
					p.LocalTools.ToolCallbacksConfirm,
					sc,
					p.SmitheryTools.SmitheryProfileID,
					allSmitheryTools,
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
		writer.verbose(
			verboseMaximum,
			p.Verbose,
			"request params without prompt: %s\n\n",
			prettify(p.redact()),
		)

		if p.Caching.CacheContext { // cache context
			return cacheContext(context.TODO(),
				writer,
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
				writer,
				conf.TimeoutSeconds,
				*p.Configuration.GoogleAIAPIKey,
				p.Verbose,
			)
		} else if p.Caching.DeleteCachedContext != nil { // delete cached context
			return deleteCachedContext(context.TODO(),
				writer,
				conf.TimeoutSeconds,
				*p.Configuration.GoogleAIAPIKey,
				*p.Caching.DeleteCachedContext,
				p.Verbose,
			)
		} else if p.ListModels { // list models
			return listModels(context.TODO(),
				writer,
				conf.TimeoutSeconds,
				*p.Configuration.GoogleAIAPIKey,
				p.Verbose,
			)
		} else { // otherwise, (should not reach here)
			writer.print(
				verboseMedium,
				"Parameter error: no task was requested or handled properly.",
			)

			return writer.printHelpBeforeExit(1, parser), nil
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
