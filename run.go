// run.go

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jessevdk/go-flags"

	gt "github.com/meinside/gemini-things-go"
)

// run with params
func run(parser *flags.Parser, p params) (exit int, err error) {
	// early return if no task was requested
	if !p.taskRequested() {
		logMessage(verboseMedium, "No task was requested.")

		return printHelpBeforeExit(1, parser), nil
	}

	// read and apply configs
	var conf config
	if conf, err = readConfig(resolveConfigFilepath(p.Configuration.ConfigFilepath)); err == nil {
		if p.Generation.SystemInstruction == nil && conf.SystemInstruction != nil {
			p.Generation.SystemInstruction = conf.SystemInstruction
		}
	} else {
		return 1, fmt.Errorf("failed to read configuration: %w", err)
	}

	// override parameters with command arguments
	if conf.GoogleAIAPIKey != nil && p.Configuration.GoogleAIAPIKey == nil {
		p.Configuration.GoogleAIAPIKey = conf.GoogleAIAPIKey
	}
	if conf.GoogleAIModel != nil && p.Configuration.GoogleAIModel == nil {
		p.Configuration.GoogleAIModel = conf.GoogleAIModel
	}
	if conf.GoogleAIImageGenerationModel != nil && p.Configuration.GoogleAIImageGenerationModel == nil {
		p.Configuration.GoogleAIImageGenerationModel = conf.GoogleAIImageGenerationModel
	}
	if conf.GoogleAIEmbeddingsModel != nil && p.Configuration.GoogleAIEmbeddingsModel == nil {
		p.Configuration.GoogleAIEmbeddingsModel = conf.GoogleAIEmbeddingsModel
	}

	// set default values
	if p.Configuration.GoogleAIModel == nil {
		p.Configuration.GoogleAIModel = ptr(defaultGoogleAIModel)
	}
	if p.Configuration.GoogleAIImageGenerationModel == nil {
		p.Configuration.GoogleAIImageGenerationModel = ptr(defaultGoogleAIImageGenerationModel)
	}
	if p.Configuration.GoogleAIEmbeddingsModel == nil {
		p.Configuration.GoogleAIEmbeddingsModel = ptr(defaultGoogleAIEmbeddingsModel)
	}
	if p.Generation.SystemInstruction == nil {
		p.Generation.SystemInstruction = ptr(defaultSystemInstruction(p))
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
		return 1, fmt.Errorf("failed to read given filepaths: %w", err)
	}

	if p.hasPrompt() { // if prompt is given,
		logVerbose(verboseMaximum, p.Verbose, "request params with prompt: %s\n\n", prettify(p.redact()))

		if p.Embeddings.GenerateEmbeddings { // generate embeddings with given prompt,
			return doEmbeddingsGeneration(context.TODO(),
				conf.TimeoutSeconds,
				*p.Configuration.GoogleAIAPIKey,
				*p.Configuration.GoogleAIEmbeddingsModel,
				*p.Generation.Prompt,
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

				logVerbose(verboseMedium, p.Verbose, "replaced prompt: %s\n\nresulting prompts: %v\n\n", replacedPrompt, prompts)
			} else {
				// or, use the given prompt as it is,
				prompts = append(prompts, gt.PromptFromText(*p.Generation.Prompt))
			}

			if p.Caching.CacheContext { // cache context
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
				var model string
				if p.Generation.GenerateImages {
					model = *p.Configuration.GoogleAIImageGenerationModel
				} else {
					model = *p.Configuration.GoogleAIModel
				}

				return doGeneration(context.TODO(),
					conf.TimeoutSeconds,
					*p.Configuration.GoogleAIAPIKey,
					model,
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
					p.Generation.OutputAsJSON,
					p.Generation.GenerateImages,
					p.Generation.SaveImagesToFiles,
					p.Generation.SaveImagesToDir,
					!p.ErrorOnUnsupportedType,
					p.Verbose,
				)
			}
		}
	} else { // if prompt is not given,
		logVerbose(verboseMaximum, p.Verbose, "request params without prompt: %s\n\n", prettify(p.redact()))

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
			logMessage(verboseMedium, "Parameter error: no task was requested or handled properly.")

			return printHelpBeforeExit(1, parser), nil
		}
	}
}

// generate a default system instruction with given params
func defaultSystemInstruction(p params) string {
	datetime := time.Now().Format("2006-01-02 15:04:05 MST (Mon)")
	hostname, _ := os.Hostname()

	return fmt.Sprintf(defaultSystemInstructionFormat,
		appName,
		*p.Configuration.GoogleAIModel,
		datetime,
		hostname,
	)
}
