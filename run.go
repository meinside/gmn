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
	if conf, err = readConfig(resolveConfigFilepath(p.ConfigFilepath)); err == nil {
		if p.SystemInstruction == nil && conf.SystemInstruction != nil {
			p.SystemInstruction = conf.SystemInstruction
		}
	} else {
		return 1, fmt.Errorf("failed to read configuration: %w", err)
	}

	// override parameters with command arguments
	if conf.GoogleAIAPIKey != nil && p.GoogleAIAPIKey == nil {
		p.GoogleAIAPIKey = conf.GoogleAIAPIKey
	}
	if conf.GoogleAIModel != nil && p.GoogleAIModel == nil {
		p.GoogleAIModel = conf.GoogleAIModel
	}
	if conf.GoogleAIImageGenerationModel != nil && p.GoogleAIImageGenerationModel == nil {
		p.GoogleAIImageGenerationModel = conf.GoogleAIImageGenerationModel
	}
	if conf.GoogleAIEmbeddingsModel != nil && p.GoogleAIEmbeddingsModel == nil {
		p.GoogleAIEmbeddingsModel = conf.GoogleAIEmbeddingsModel
	}

	// set default values
	if p.GoogleAIModel == nil {
		p.GoogleAIModel = ptr(defaultGoogleAIModel)
	}
	if p.GoogleAIImageGenerationModel == nil {
		p.GoogleAIImageGenerationModel = ptr(defaultGoogleAIImageGenerationModel)
	}
	if p.GoogleAIEmbeddingsModel == nil {
		p.GoogleAIEmbeddingsModel = ptr(defaultGoogleAIEmbeddingsModel)
	}
	if p.SystemInstruction == nil {
		p.SystemInstruction = ptr(defaultSystemInstruction(p))
	}
	if p.UserAgent == nil {
		p.UserAgent = ptr(defaultUserAgent)
	}

	// check existence of essential parameters here
	if conf.GoogleAIAPIKey == nil {
		return 1, fmt.Errorf("google AI API Key is missing")
	}

	// expand filepaths (recurse directories)
	p.Filepaths, err = expandFilepaths(p)
	if err != nil {
		return 1, fmt.Errorf("failed to read given filepaths: %w", err)
	}

	if p.hasPrompt() { // if prompt is given,
		logVerbose(verboseMaximum, p.Verbose, "request params with prompt: %s\n\n", prettify(p.redact()))

		if p.GenerateEmbeddings { // generate embeddings with given prompt,
			return doEmbeddingsGeneration(context.TODO(),
				conf.TimeoutSeconds,
				*p.GoogleAIAPIKey,
				*p.GoogleAIEmbeddingsModel,
				*p.Prompt,
				p.EmbeddingsChunkSize,
				p.EmbeddingsOverlappedChunkSize,
				p.Verbose,
			)
		} else {
			prompts := []gt.Prompt{}
			promptFiles := map[string][]byte{}

			if p.ReplaceHTTPURLsInPrompt {
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
				prompts = append(prompts, gt.PromptFromText(*p.Prompt))
			}

			if p.CacheContext { // cache context
				return cacheContext(context.TODO(),
					conf.TimeoutSeconds,
					*p.GoogleAIAPIKey,
					*p.GoogleAIModel,
					*p.SystemInstruction,
					prompts,
					promptFiles,
					p.Filepaths,
					p.CachedContextName,
					p.Verbose,
				)
			} else { // generate
				var model string
				if p.GenerateImages {
					model = *p.GoogleAIImageGenerationModel
				} else {
					model = *p.GoogleAIModel
				}

				return doGeneration(context.TODO(),
					conf.TimeoutSeconds,
					*p.GoogleAIAPIKey,
					model,
					*p.SystemInstruction,
					p.Temperature,
					p.TopP,
					p.TopK,
					prompts,
					promptFiles,
					p.Filepaths,
					p.ThinkingOn,
					p.ThinkingBudget,
					p.GroundingOn,
					p.CachedContextName,
					p.OutputAsJSON,
					p.GenerateImages,
					p.SaveImagesToFiles,
					p.SaveImagesToDir,
					!p.ErrorOnUnsupportedType,
					p.Verbose,
				)
			}
		}
	} else { // if prompt is not given,
		logVerbose(verboseMaximum, p.Verbose, "request params without prompt: %s\n\n", prettify(p.redact()))

		if p.CacheContext { // cache context
			return cacheContext(context.TODO(),
				conf.TimeoutSeconds,
				*p.GoogleAIAPIKey,
				*p.GoogleAIModel,
				*p.SystemInstruction,
				nil, // prompt not given
				nil, // prompt not given
				p.Filepaths,
				p.CachedContextName,
				p.Verbose,
			)
		} else if p.ListCachedContexts { // list cached contexts
			return listCachedContexts(context.TODO(),
				conf.TimeoutSeconds,
				*p.GoogleAIAPIKey,
				p.Verbose,
			)
		} else if p.DeleteCachedContext != nil { // delete cached context
			return deleteCachedContext(context.TODO(),
				conf.TimeoutSeconds,
				*p.GoogleAIAPIKey,
				*p.DeleteCachedContext,
				p.Verbose,
			)
		} else if p.ListModels { // list models
			return listModels(context.TODO(),
				conf.TimeoutSeconds,
				*p.GoogleAIAPIKey,
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
		*p.GoogleAIModel,
		datetime,
		hostname,
	)
}
