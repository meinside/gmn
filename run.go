// run.go

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jessevdk/go-flags"
)

const (
	defaultConfigFilename = "config.json"

	defaultGoogleAIModel           = "gemini-2.0-flash-001"
	defaultGoogleAIEmbeddingsModel = "gemini-embedding-exp-03-07"

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
	defaultUserAgent              = `GMN/fetcher`
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
	if conf.GoogleAIEmbeddingsModel != nil && p.GoogleAIEmbeddingsModel == nil {
		p.GoogleAIEmbeddingsModel = conf.GoogleAIEmbeddingsModel
	}

	// set default values
	if p.GoogleAIModel == nil {
		p.GoogleAIModel = ptr(defaultGoogleAIModel)
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
		// replace urls in the prompt
		replacedPrompt := *p.Prompt
		promptFiles := map[string][]byte{}
		if p.ReplaceHTTPURLsInPrompt {
			replacedPrompt, promptFiles = replaceURLsInPrompt(conf, p)
			p.Prompt = &replacedPrompt

			logVerbose(verboseMedium, p.Verbose, "replaced prompt: %s\n\n", replacedPrompt)
		}

		logVerbose(verboseMaximum, p.Verbose, "request params with prompt: %s\n\n", prettify(p.redact()))

		if p.CacheContext { // cache context
			// cache context
			return cacheContext(context.TODO(),
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
			if !p.GenerateEmbeddings {
				return doGeneration(context.TODO(),
					conf.TimeoutSeconds,
					*p.GoogleAIAPIKey,
					*p.GoogleAIModel,
					*p.SystemInstruction,
					p.Temperature,
					p.TopP,
					p.TopK,
					*p.Prompt,
					promptFiles,
					p.Filepaths,
					p.CachedContextName,
					p.OutputAsJSON,
					p.GenerateImages,
					p.SaveImagesToFiles,
					p.SaveImagesToDir,
					p.Verbose)
			} else {
				return doEmbeddingsGeneration(context.TODO(),
					conf.TimeoutSeconds,
					*p.GoogleAIAPIKey,
					*p.GoogleAIEmbeddingsModel,
					*p.Prompt,
					p.EmbeddingsChunkSize,
					p.EmbeddingsOverlappedChunkSize,
					p.Verbose)
			}
		}
	} else { // if prompt is not given
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
				p.Verbose)
		} else if p.ListCachedContexts { // list cached contexts
			return listCachedContexts(context.TODO(),
				conf.TimeoutSeconds,
				*p.GoogleAIAPIKey,
				*p.GoogleAIModel,
				p.Verbose)
		} else if p.DeleteCachedContext != nil { // delete cached context
			return deleteCachedContext(context.TODO(),
				conf.TimeoutSeconds,
				*p.GoogleAIAPIKey,
				*p.GoogleAIModel,
				*p.DeleteCachedContext,
				p.Verbose)
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
