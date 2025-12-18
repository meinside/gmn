// run.go
//
// Things for running this application.

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jessevdk/go-flags"
	"google.golang.org/genai"

	gt "github.com/meinside/gemini-things-go"
	"github.com/meinside/version-go"
)

// modelPurpose represents the purpose of a model.
type modelPurpose string

const (
	modelForEmbeddings       modelPurpose = "embeddings"
	modelForImageGeneration  modelPurpose = "image_generation"
	modelForSpeechGeneration modelPurpose = "speech_generation"
	modelForGeneralPurpose   modelPurpose = ""
)

// resolveGoogleAIModel resolves the appropriate Google AI model based on the purpose.
func resolveGoogleAIModel(
	p *params,
	conf *config,
	purpose modelPurpose,
) *string {
	if p.Configuration.GoogleAIModel != nil {
		return p.Configuration.GoogleAIModel
	}

	switch purpose {
	case modelForEmbeddings:
		if conf.GoogleAIEmbeddingsModel != nil {
			return conf.GoogleAIEmbeddingsModel
		}
		return ptr(defaultGoogleAIEmbeddingsModel)
	case modelForImageGeneration:
		if conf.GoogleAIImageGenerationModel != nil {
			return conf.GoogleAIImageGenerationModel
		}
		return ptr(defaultGoogleAIImageGenerationModel)
	case modelForSpeechGeneration:
		if conf.GoogleAISpeechGenerationModel != nil {
			return conf.GoogleAISpeechGenerationModel
		}
		return ptr(defaultGoogleAISpeechGenerationModel)
	default: // general generation
		if conf.GoogleAIModel != nil {
			return conf.GoogleAIModel
		}
		return ptr(defaultGoogleAIModel)
	}
}

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
		// check if environment variable for api key exists,
		if envAPIKey, exists := os.LookupEnv(envVarNameAPIKey); exists {
			// use it,
			p.Configuration.GoogleAIAPIKey = &envAPIKey
		} else {
			// or return an error
			return 1, fmt.Errorf(
				"failed to read configuration: %w",
				err,
			)
		}
	}

	// override command arguments with values from configs
	if conf.GoogleAIAPIKey != nil && p.Configuration.GoogleAIAPIKey == nil {
		p.Configuration.GoogleAIAPIKey = conf.GoogleAIAPIKey
	}

	// fallback to default values
	if p.Generation.SystemInstruction == nil {
		p.Generation.SystemInstruction = ptr(defaultSystemInstruction())
	}
	if p.Generation.UserAgent == nil {
		p.Generation.UserAgent = ptr(defaultFetchUserAgent)
	}
	if conf.TimeoutSeconds <= 0 {
		conf.TimeoutSeconds = defaultTimeoutSeconds
	}
	if conf.ReplaceHTTPURLTimeoutSeconds <= 0 {
		conf.ReplaceHTTPURLTimeoutSeconds = defaultFetchURLTimeoutSeconds
	}

	// check existence of essential parameters here
	if conf.GoogleAIAPIKey == nil && p.Configuration.GoogleAIAPIKey == nil {
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
			p.Configuration.GoogleAIModel = resolveGoogleAIModel(&p, &conf, modelForEmbeddings)

			// gemini things client
			gtc, err := gtClient(
				p.Configuration.GoogleAIAPIKey,
				conf,
				gt.WithModel(*p.Configuration.GoogleAIModel),
			)
			if err != nil {
				return 1, err
			}
			defer func() {
				if err := gtc.Close(); err != nil {
					writer.error("Failed to close client: %s", err)
				}
			}()

			return doEmbeddingsGeneration(context.TODO(),
				writer,
				conf.TimeoutSeconds,
				gtc,
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
				if p.Generation.KeepURLsAsIs {
					return 1, fmt.Errorf("cannot use `--keep-urls` with `--convert-urls`")
				}

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
				p.Configuration.GoogleAIModel = resolveGoogleAIModel(&p, &conf, modelForGeneralPurpose)

				// gemini things client
				gtc, err := gtClient(
					p.Configuration.GoogleAIAPIKey,
					conf,
					gt.WithModel(*p.Configuration.GoogleAIModel),
				)
				if err != nil {
					return 1, err
				}
				defer func() {
					if err := gtc.Close(); err != nil {
						writer.error(
							"Failed to close client: %s",
							err,
						)
					}
				}()

				return cacheContext(context.TODO(),
					writer,
					conf.TimeoutSeconds,
					gtc,
					*p.Generation.SystemInstruction,
					prompts,
					promptFiles,
					p.Generation.Filepaths,
					p.OverrideFileMIMEType,
					p.Caching.CachedContextName,
					p.Verbose,
				)
			} else { // generate
				// model
				if p.Generation.GenerateImages {
					p.Configuration.GoogleAIModel = resolveGoogleAIModel(&p, &conf, modelForImageGeneration)
				} else if p.Generation.GenerateSpeech {
					p.Configuration.GoogleAIModel = resolveGoogleAIModel(&p, &conf, modelForSpeechGeneration)
				} else {
					p.Configuration.GoogleAIModel = resolveGoogleAIModel(&p, &conf, modelForGeneralPurpose)
				}

				var tools []genai.Tool

				// function call (local)
				if err := unmarshalJSONFromBytes(p.LocalTools.Tools, &tools); err != nil {
					return 1, fmt.Errorf("failed to read tools: %w", err)
				}

				var toolConfig *genai.ToolConfig
				if err := unmarshalJSONFromBytes(p.LocalTools.ToolConfig, &toolConfig); err != nil {
					return 1, fmt.Errorf("failed to read tool config: %w", err)
				}

				// function call (MCP)
				allMCPConnections := make(mcpConnectionsAndTools)
				defer func() {
					for _, connDetails := range allMCPConnections {
						_ = connDetails.connection.Close()
					}
				}()

				// from streamable http urls
				for _, serverURL := range p.MCPTools.StreamableHTTPURLs {
					ctx, cancel := context.WithTimeout(
						context.TODO(),
						mcpDefaultDialTimeoutSeconds*time.Second,
					)
					defer cancel()

					connDetails, err := fetchAndRegisterMCPTools(
						ctx,
						writer,
						p,
						mcpServerStreamable,
						serverURL,
					)
					if err != nil {
						return 1, err
					}
					allMCPConnections[serverURL] = *connDetails
				}

				// from local commands
				for _, cmdline := range p.MCPTools.STDIOCommands {
					ctx, cancel := context.WithTimeout(
						context.TODO(),
						mcpDefaultDialTimeoutSeconds*time.Second,
					)
					defer cancel()

					connDetails, err := fetchAndRegisterMCPTools(
						ctx,
						writer,
						p,
						mcpServerStdio,
						cmdline,
					)
					if err != nil {
						return 1, err
					}
					allMCPConnections[cmdline] = *connDetails
				}

				// attach self as a MCP tool
				if p.MCPTools.WithSelfAsSTDIOCommand {
					ctx, cancel := context.WithTimeout(
						context.TODO(),
						mcpDefaultDialTimeoutSeconds*time.Second,
					)
					defer cancel()

					if connDetails, err := selfAsMCPTool(ctx, conf, p, writer); err == nil {
						allMCPConnections[mcpToolNameSelf] = *connDetails
					} else {
						return 1, fmt.Errorf("failed to run self as a local MCP tool: %w", err)
					}
				}

				// check for duplicated function names after all tools are collected
				if value, duplicated := duplicated(
					keysFromTools(tools, allMCPConnections),
				); duplicated {
					return 1, fmt.Errorf(
						"duplicated function name in tools: '%s'",
						value,
					)
				}

				// generate with file search
				if len(p.Generation.FileSearchStores) > 0 {
					tools = append(tools, genai.Tool{
						FileSearch: &genai.FileSearch{
							FileSearchStoreNames: p.Generation.FileSearchStores,
						},
					})
				}

				// check if prompt has any http url in it,
				if !p.Generation.KeepURLsAsIs {
					if urlsInPrompt(p) && !p.Generation.GenerateImages && !p.Generation.GenerateSpeech {
						tools = append(tools, genai.Tool{
							URLContext: &genai.URLContext{},
						})
					}
				}

				// gemini things client
				gtc, err := gtClient(
					p.Configuration.GoogleAIAPIKey,
					conf,
					gt.WithModel(*p.Configuration.GoogleAIModel),
				)
				if err != nil {
					return 1, err
				}
				defer func() {
					if err := gtc.Close(); err != nil {
						writer.error("Failed to close client: %s", err)
					}
				}()

				return doGeneration(
					context.TODO(),
					writer,
					conf.TimeoutSeconds,
					gtc,
					*p.Generation.SystemInstruction,
					p.Generation.Temperature,
					p.Generation.TopP,
					p.Generation.TopK,
					prompts,
					promptFiles,
					p.Generation.Filepaths,
					p.OverrideFileMIMEType,
					p.Generation.ThinkingOn,
					p.Generation.ThinkingBudget,
					p.Generation.ShowThinking,
					p.Generation.GroundingOn,
					p.Generation.WithGoogleMaps, p.Generation.GoogleMapsLatitude, p.Generation.GoogleMapsLongitude,
					p.Caching.CachedContextName,
					p.Tools.ShowCallbackResults,
					p.Tools.RecurseOnCallbackResults,
					p.Tools.MaxCallbackLoopCount,
					p.Tools.ForceCallDestructiveTools,
					tools,
					toolConfig,
					p.LocalTools.ToolCallbacks,
					p.LocalTools.ToolCallbacksConfirm,
					allMCPConnections,
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
			// gemini things client
			gtc, err := gtClient(
				p.Configuration.GoogleAIAPIKey,
				conf,
				gt.WithModel(*p.Configuration.GoogleAIModel),
			)
			if err != nil {
				return 1, err
			}
			defer func() {
				if err := gtc.Close(); err != nil {
					writer.error("Failed to close client: %s", err)
				}
			}()

			return cacheContext(
				context.TODO(),
				writer,
				conf.TimeoutSeconds,
				gtc,
				*p.Generation.SystemInstruction,
				nil, // prompt not given
				nil, // prompt not given
				p.Generation.Filepaths,
				p.OverrideFileMIMEType,
				p.Caching.CachedContextName,
				p.Verbose,
			)
		} else if p.Caching.ListCachedContexts { // list cached contexts
			// gemini things client
			gtc, err := gtClient(p.Configuration.GoogleAIAPIKey, conf)
			if err != nil {
				return 1, err
			}
			defer func() {
				if err := gtc.Close(); err != nil {
					writer.error(
						"Failed to close client: %s",
						err,
					)
				}
			}()

			return listCachedContexts(
				context.TODO(),
				writer,
				conf.TimeoutSeconds,
				gtc,
				p.Verbose,
			)
		} else if p.Caching.DeleteCachedContext != nil { // delete cached context
			// gemini things client
			gtc, err := gtClient(p.Configuration.GoogleAIAPIKey, conf)
			if err != nil {
				return 1, err
			}
			defer func() {
				if err := gtc.Close(); err != nil {
					writer.error(
						"Failed to close client: %s",
						err,
					)
				}
			}()

			return deleteCachedContext(
				context.TODO(),
				writer,
				conf.TimeoutSeconds,
				gtc,
				*p.Caching.DeleteCachedContext,
				p.Verbose,
			)
		} else if p.ListModels { // list models
			// gemini things client
			gtc, err := gtClient(p.Configuration.GoogleAIAPIKey, conf)
			if err != nil {
				return 1, err
			}
			defer func() {
				if err := gtc.Close(); err != nil {
					writer.error(
						"Failed to close client: %s",
						err,
					)
				}
			}()

			return listModels(
				context.TODO(),
				writer,
				conf.TimeoutSeconds,
				gtc,
				p.Verbose,
			)
		} else if p.FileSearch.ListFileSearchStores { // list file search stores
			// gemini things client
			gtc, err := gtClient(p.Configuration.GoogleAIAPIKey, conf)
			if err != nil {
				return 1, err
			}
			defer func() {
				if err := gtc.Close(); err != nil {
					writer.error("Failed to close client: %s", err)
				}
			}()

			return listFileSearchStores(
				context.TODO(),
				writer,
				conf.TimeoutSeconds,
				gtc,
				p.Verbose,
			)
		} else if p.FileSearch.CreateFileSearchStore != nil { // create file search store
			// gemini things client
			gtc, err := gtClient(p.Configuration.GoogleAIAPIKey, conf)
			if err != nil {
				return 1, err
			}
			defer func() {
				if err := gtc.Close(); err != nil {
					writer.error(
						"Failed to close client: %s",
						err,
					)
				}
			}()

			return createFileSearchStore(
				context.TODO(),
				writer,
				conf.TimeoutSeconds,
				gtc,
				*p.FileSearch.CreateFileSearchStore,
				p.Verbose,
			)
		} else if p.FileSearch.DeleteFileSearchStore != nil { // delete file search store
			// gemini things client
			gtc, err := gtClient(p.Configuration.GoogleAIAPIKey, conf)
			if err != nil {
				return 1, err
			}
			defer func() {
				if err := gtc.Close(); err != nil {
					writer.error(
						"Failed to close client: %s",
						err,
					)
				}
			}()

			return deleteFileSearchStore(
				context.TODO(),
				writer,
				conf.TimeoutSeconds,
				gtc,
				*p.FileSearch.DeleteFileSearchStore,
				p.Verbose,
			)
		} else if p.FileSearch.FileSearchStoreNameToUploadFiles != nil { // upload files to file search store
			if len(p.Generation.Filepaths) > 0 {
				if files, err := openFilesForPrompt(nil, p.Generation.Filepaths); err == nil {
					// close files
					defer func() {
						for _, toClose := range files {
							if err := toClose.Close(); err != nil {
								writer.error(
									"Failed to close file: %s",
									err,
								)
							}
						}
					}()

					filepaths := make([]string, len(files))
					for i, file := range files {
						filepaths[i] = file.filepath
					}

					// gemini things client
					gtc, err := gtClient(p.Configuration.GoogleAIAPIKey, conf)
					if err != nil {
						return 1, err
					}
					defer func() {
						if err := gtc.Close(); err != nil {
							writer.error(
								"Failed to close client: %s",
								err,
							)
						}
					}()

					return uploadFilesToFileSearchStore(
						context.TODO(),
						writer,
						conf.TimeoutSeconds,
						gtc,
						*p.FileSearch.FileSearchStoreNameToUploadFiles,
						filepaths,
						p.Embeddings.EmbeddingsChunkSize,
						p.Embeddings.EmbeddingsOverlappedChunkSize,
						p.OverrideFileMIMEType,
						p.Verbose,
					)

				} else {
					return 1, fmt.Errorf("failed to open files for file search: %s", err)
				}
			} else {
				return 1, fmt.Errorf("no file was given for file search store '%s'", *p.FileSearch.FileSearchStoreNameToUploadFiles)
			}
		} else if p.FileSearch.ListFilesInFileSearchStore != nil { // list files in file search store
			// gemini things client
			gtc, err := gtClient(p.Configuration.GoogleAIAPIKey, conf)
			if err != nil {
				return 1, err
			}
			defer func() {
				if err := gtc.Close(); err != nil {
					writer.error(
						"Failed to close client: %s",
						err,
					)
				}
			}()

			return listFilesInFileSearchStore(
				context.TODO(),
				writer,
				conf.TimeoutSeconds,
				gtc,
				*p.FileSearch.ListFilesInFileSearchStore,
				p.Verbose,
			)
		} else if p.FileSearch.DeleteFileInFileSearchStore != nil { // delete a file in a file search store
			// gemini things client
			gtc, err := gtClient(p.Configuration.GoogleAIAPIKey, conf)
			if err != nil {
				return 1, err
			}
			defer func() {
				if err := gtc.Close(); err != nil {
					writer.error(
						"Failed to close client: %s",
						err,
					)
				}
			}()

			return deleteFileInFileSearchStore(
				context.TODO(),
				writer,
				conf.TimeoutSeconds,
				gtc,
				*p.FileSearch.DeleteFileInFileSearchStore,
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
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown-host" // Fallback if hostname cannot be retrieved
	}

	return fmt.Sprintf(defaultSystemInstructionFormat,
		appName,
		datetime,
		hostname,
	)
}
