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
	modelForVideoGeneration  modelPurpose = "video_generation"
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
		return new(defaultGoogleAIEmbeddingsModel)
	case modelForImageGeneration:
		if conf.GoogleAIImageGenerationModel != nil {
			return conf.GoogleAIImageGenerationModel
		}
		return new(defaultGoogleAIImageGenerationModel)
	case modelForVideoGeneration:
		if conf.GoogleAIVideoGenerationModel != nil {
			return conf.GoogleAIVideoGenerationModel
		}
		return new(defaultGoogleAIVideoGenerationModel)
	case modelForSpeechGeneration:
		if conf.GoogleAISpeechGenerationModel != nil {
			return conf.GoogleAISpeechGenerationModel
		}
		return new(defaultGoogleAISpeechGenerationModel)
	default: // general generation
		if conf.GoogleAIModel != nil {
			return conf.GoogleAIModel
		}
		return new(defaultGoogleAIModel)
	}
}

// withGTClient creates a gt.Client, runs the given function, and ensures the client is closed.
func withGTClient(
	writer outputWriter,
	conf config,
	fn func(gtc *gt.Client) (int, error),
	options ...gt.ClientOption,
) (int, error) {
	gtc, err := gtClient(conf, options...)
	if err != nil {
		return 1, err
	}
	defer func() {
		if err := gtc.Close(); err != nil {
			writer.error("Failed to close client: %s", err)
		}
	}()

	return fn(gtc)
}

// run with params
func run(
	parser *flags.Parser,
	writer outputWriter,
	p params,
) (exit int, err error) {
	// early return if no task was requested
	if !p.taskRequested() {
		writer.printWithColorForLevel(
			verboseMedium,
			"No task was requested.\n\n",
		)

		return writer.printHelpBeforeExit(1, parser), nil
	}

	// early return after printing the version
	if p.ShowVersion {
		writer.printWithColorForLevel(
			verboseMinimum,
			"%s %s\n\n",
			appName,
			version.Build(version.OS|version.Architecture),
		)

		return writer.printHelpBeforeExit(0, parser), nil
	}

	// read and apply configs
	var conf config
	if conf, p, err = readAndFillConfig(p, writer); err != nil {
		return 1, fmt.Errorf("failed to read and fill configs: %w", err)
	}

	// expand filepaths (recurse directories)
	p.Generation.Filepaths, err = expandFilepaths(writer, p)
	if err != nil {
		return 1, fmt.Errorf(
			"failed to read given filepaths: %w",
			err,
		)
	}

	if p.hasPrompt() {
		return runWithPrompt(writer, conf, p)
	}

	return runWithoutPrompt(parser, writer, conf, p)
}

// runWithPrompt handles all prompt-based tasks.
func runWithPrompt(
	writer outputWriter,
	conf config,
	p params,
) (int, error) {
	writer.verbose(
		verboseMaximum,
		p.Verbose,
		"request params with prompt: %s\n\n",
		prettify(p.redact()),
	)

	// generate embeddings
	if p.Embeddings.GenerateEmbeddings {
		p.Configuration.GoogleAIModel = resolveGoogleAIModel(&p, &conf, modelForEmbeddings)

		return withGTClient(writer, conf, func(gtc *gt.Client) (int, error) {
			return doEmbeddingsGeneration(context.TODO(),
				writer,
				conf.TimeoutSeconds,
				gtc,
				p,
			)
		}, gt.WithModel(*p.Configuration.GoogleAIModel))
	}

	// prepare prompts (shared by cache context and generation)
	prompts, promptFiles, err := preparePrompts(writer, conf, p)
	if err != nil {
		return 1, err
	}

	// cache context with prompt
	if p.Caching.CacheContext {
		p.Configuration.GoogleAIModel = resolveGoogleAIModel(&p, &conf, modelForGeneralPurpose)

		return withGTClient(writer, conf, func(gtc *gt.Client) (int, error) {
			return cacheContext(context.TODO(),
				writer,
				conf.TimeoutSeconds,
				gtc,
				prompts,
				promptFiles,
				p,
			)
		}, gt.WithModel(*p.Configuration.GoogleAIModel))
	}

	// generate (text, image, video, speech)
	return runGeneration(writer, conf, p, prompts, promptFiles)
}

// preparePrompts builds prompts and prompt files from the given params.
func preparePrompts(
	writer outputWriter,
	conf config,
	p params,
) (prompts []gt.Prompt, promptFiles map[string][]byte, err error) {
	prompts = []gt.Prompt{}
	promptFiles = map[string][]byte{}

	if p.Generation.FetchContents.ReplaceHTTPURLsInPrompt {
		if p.Generation.FetchContents.KeepURLsAsIs {
			return nil, nil, fmt.Errorf("cannot use `--keep-urls` with `--convert-urls`")
		}

		// replace urls in the prompt,
		replacedPrompt, extractedFiles := replaceURLsInPrompt(writer, conf, p)

		prompts = append(prompts, gt.PromptFromText(replacedPrompt))

		for customURL, data := range extractedFiles {
			if customURL.isLink() {
				promptFiles[customURL.url()] = data
			} else if customURL.isYoutube() {
				prompts = append(prompts, gt.PromptFromURI(customURL.url(), "video/mp4"))
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

	return prompts, promptFiles, nil
}

// runGeneration handles the generation sub-path (text, image, video, speech).
func runGeneration(
	writer outputWriter,
	conf config,
	p params,
	prompts []gt.Prompt,
	promptFiles map[string][]byte,
) (int, error) {
	// resolve model
	switch {
	case p.Generation.Image.GenerateImages:
		p.Configuration.GoogleAIModel = resolveGoogleAIModel(&p, &conf, modelForImageGeneration)
	case p.Generation.Video.GenerateVideos:
		p.Configuration.GoogleAIModel = resolveGoogleAIModel(&p, &conf, modelForVideoGeneration)
	case p.Generation.Speech.GenerateSpeech:
		p.Configuration.GoogleAIModel = resolveGoogleAIModel(&p, &conf, modelForSpeechGeneration)
	default:
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
	if len(p.Generation.DetailedOptions.FileSearchStores) > 0 {
		tools = append(tools, genai.Tool{
			FileSearch: &genai.FileSearch{
				FileSearchStoreNames: p.Generation.DetailedOptions.FileSearchStores,
			},
		})
	}

	// check if prompt has any http url in it,
	if !p.Generation.FetchContents.KeepURLsAsIs {
		if urlsInPrompt(p) &&
			!p.Generation.Image.GenerateImages &&
			!p.Generation.Video.GenerateVideos &&
			!p.Generation.Speech.GenerateSpeech {
			tools = append(tools, genai.Tool{
				URLContext: &genai.URLContext{},
			})
		}
	}

	// gemini things client
	return withGTClient(writer, conf, func(gtc *gt.Client) (int, error) {
		if len(p.Verbose) > 3 {
			writer.warn("Full verbose mode: %d > 3", len(p.Verbose))

			gtc.Verbose = true
		}

		return doGeneration(
			context.TODO(),
			writer,
			conf.TimeoutSeconds,
			gtc,
			nil, // NOTE: first call => no history
			prompts,
			promptFiles,
			tools,
			toolConfig,
			allMCPConnections,
			nil, // NOTE: first call => no thought signature
			p,
		)
	}, gt.WithModel(*p.Configuration.GoogleAIModel))
}

// runWithoutPrompt handles all non-prompt tasks.
func runWithoutPrompt(
	parser *flags.Parser,
	writer outputWriter,
	conf config,
	p params,
) (int, error) {
	writer.verbose(
		verboseMaximum,
		p.Verbose,
		"request params without prompt: %s\n\n",
		prettify(p.redact()),
	)

	// cache context (files only)
	if p.Caching.CacheContext {
		return withGTClient(writer, conf, func(gtc *gt.Client) (int, error) {
			return cacheContext(
				context.TODO(),
				writer,
				conf.TimeoutSeconds,
				gtc,
				nil, // prompt not given
				nil, // prompt not given
				p,
			)
		}, gt.WithModel(*p.Configuration.GoogleAIModel))
	}

	// list cached contexts
	if p.Caching.ListCachedContexts {
		return withGTClient(writer, conf, func(gtc *gt.Client) (int, error) {
			return listCachedContexts(
				context.TODO(),
				writer,
				conf.TimeoutSeconds,
				gtc,
				p,
			)
		})
	}

	// delete cached context
	if p.Caching.DeleteCachedContext != nil {
		return withGTClient(writer, conf, func(gtc *gt.Client) (int, error) {
			return deleteCachedContext(
				context.TODO(),
				writer,
				conf.TimeoutSeconds,
				gtc,
				p,
			)
		})
	}

	// list models
	if p.ListModels {
		return withGTClient(writer, conf, func(gtc *gt.Client) (int, error) {
			return listModels(
				context.TODO(),
				writer,
				conf.TimeoutSeconds,
				gtc,
				p,
			)
		})
	}

	// generate embeddings without a prompt
	if p.Embeddings.GenerateEmbeddings {
		p.Configuration.GoogleAIModel = resolveGoogleAIModel(&p, &conf, modelForEmbeddings)

		return withGTClient(writer, conf, func(gtc *gt.Client) (int, error) {
			return doEmbeddingsGeneration(context.TODO(),
				writer,
				conf.TimeoutSeconds,
				gtc,
				p,
			)
		}, gt.WithModel(*p.Configuration.GoogleAIModel))
	}

	// list file search stores
	if p.FileSearch.ListFileSearchStores {
		return withGTClient(writer, conf, func(gtc *gt.Client) (int, error) {
			return listFileSearchStores(
				context.TODO(),
				writer,
				conf.TimeoutSeconds,
				gtc,
				p,
			)
		})
	}

	// create file search store
	if p.FileSearch.CreateFileSearchStore != nil {
		return withGTClient(writer, conf, func(gtc *gt.Client) (int, error) {
			return createFileSearchStore(
				context.TODO(),
				writer,
				conf.TimeoutSeconds,
				gtc,
				p,
			)
		})
	}

	// delete file search store
	if p.FileSearch.DeleteFileSearchStore != nil {
		return withGTClient(writer, conf, func(gtc *gt.Client) (int, error) {
			return deleteFileSearchStore(
				context.TODO(),
				writer,
				conf.TimeoutSeconds,
				gtc,
				p,
			)
		})
	}

	// upload files to file search store
	if p.FileSearch.FileSearchStoreNameToUploadFiles != nil {
		return runUploadToFileSearchStore(writer, conf, p)
	}

	// list files in file search store
	if p.FileSearch.ListFilesInFileSearchStore != nil {
		return withGTClient(writer, conf, func(gtc *gt.Client) (int, error) {
			return listFilesInFileSearchStore(
				context.TODO(),
				writer,
				conf.TimeoutSeconds,
				gtc,
				p,
			)
		})
	}

	// delete a file in file search store
	if p.FileSearch.DeleteFileInFileSearchStore != nil {
		return withGTClient(writer, conf, func(gtc *gt.Client) (int, error) {
			return deleteFileInFileSearchStore(
				context.TODO(),
				writer,
				conf.TimeoutSeconds,
				gtc,
				p,
			)
		})
	}

	// should not reach here
	writer.printWithColorForLevel(
		verboseMedium,
		"Parameter error: no task was requested or handled properly.",
	)

	return writer.printHelpBeforeExit(1, parser), nil
}

// runUploadToFileSearchStore handles file upload to a file search store.
func runUploadToFileSearchStore(
	writer outputWriter,
	conf config,
	p params,
) (int, error) {
	if len(p.Generation.Filepaths) == 0 {
		return 1, fmt.Errorf("no file was given for file search store '%s'", *p.FileSearch.FileSearchStoreNameToUploadFiles)
	}

	files, err := openFilesForPrompt(nil, p.Generation.Filepaths)
	if err != nil {
		return 1, fmt.Errorf("failed to open files for file search: %s", err)
	}

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

	return withGTClient(writer, conf, func(gtc *gt.Client) (int, error) {
		return uploadFilesToFileSearchStore(
			context.TODO(),
			writer,
			conf.TimeoutSeconds,
			gtc,
			filepaths,
			p,
		)
	})
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
