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
		p.Generation.UserAgent = ptr(defaultFetchUserAgent)
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
					p.Configuration.GoogleAIModel = resolveGoogleAIModel(&p, &conf, modelForImageGeneration)
				} else if p.Generation.GenerateSpeech {
					p.Configuration.GoogleAIModel = resolveGoogleAIModel(&p, &conf, modelForSpeechGeneration)
				} else {
					p.Configuration.GoogleAIModel = resolveGoogleAIModel(&p, &conf, modelForGeneralPurpose)
				}

				// function call (local)
				var tools []genai.Tool
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
					ctx, cancel := context.WithTimeout(context.TODO(), mcpDefaultDialTimeoutSeconds*time.Second)
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
				for _, cmdline := range p.MCPTools.StdioCommands {
					ctx, cancel := context.WithTimeout(context.TODO(), mcpDefaultDialTimeoutSeconds*time.Second)
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

				// check for duplicated function names after all tools are collected
				if value, duplicated := duplicated(
					keysFromTools(tools, allMCPConnections),
				); duplicated {
					return 1, fmt.Errorf(
						"duplicated function name in tools: '%s'",
						value,
					)
				}

				// check if prompt has any http url in it,
				if !p.Generation.KeepURLsAsIs {
					if urlsInPrompt(p) && !p.Generation.GenerateImages && !p.Generation.GenerateSpeech {
						tools = append(tools, genai.Tool{
							URLContext: &genai.URLContext{},
						})
					}
				}

				return doGeneration(
					context.TODO(),
					writer,
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
			return cacheContext(
				context.TODO(),
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
			return listCachedContexts(
				context.TODO(),
				writer,
				conf.TimeoutSeconds,
				*p.Configuration.GoogleAIAPIKey,
				p.Verbose,
			)
		} else if p.Caching.DeleteCachedContext != nil { // delete cached context
			return deleteCachedContext(
				context.TODO(),
				writer,
				conf.TimeoutSeconds,
				*p.Configuration.GoogleAIAPIKey,
				*p.Caching.DeleteCachedContext,
				p.Verbose,
			)
		} else if p.ListModels { // list models
			return listModels(
				context.TODO(),
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
