// gemini.go
//
// things for using Gemini APIs

package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/generative-ai-go/genai"

	gt "github.com/meinside/gemini-things-go"
)

// generation parameter constants
//
// (https://ai.google.dev/gemini-api/docs/text-generation?lang=go#configure)
const (
	defaultGenerationTemperature = float32(1.0)
	defaultGenerationTopP        = float32(0.95)
	defaultGenerationTopK        = int32(20)
)

// generate text with given things
func doGeneration(
	ctx context.Context,
	timeoutSeconds int,
	googleAIAPIKey, googleAIModel string,
	systemInstruction string, temperature, topP *float32, topK *int32,
	prompt string, promptFiles map[string][]byte, filepaths []*string,
	cachedContextName *string,
	outputAsJSON bool,
	vbs []bool,
) (exit int, e error) {
	logVerbose(verboseMedium, vbs, "generating...")

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	// gemini things client
	gtc, err := gt.NewClient(googleAIAPIKey, googleAIModel)
	if err != nil {
		return 1, err
	}
	defer func() {
		if err := gtc.Close(); err != nil {
			logError("Failed to close client: %s", err)
		}
	}()

	// configure gemini things client
	gtc.SetTimeout(timeoutSeconds)
	gtc.SetSystemInstructionFunc(func() string {
		return systemInstruction
	})

	// read & close files
	files, filesToClose, err := openFilesForPrompt(promptFiles, filepaths)
	if err != nil {
		return 1, err
	}
	defer func() {
		for _, toClose := range filesToClose {
			if err := toClose.Close(); err != nil {
				logError("Failed to close file: %s", err)
			}
		}
	}()

	// generation options
	opts := gt.NewGenerationOptions()
	if cachedContextName != nil {
		opts.CachedContextName = ptr(strings.TrimSpace(*cachedContextName))
	}
	generationTemperature := defaultGenerationTemperature
	if temperature != nil {
		generationTemperature = *temperature
	}
	generationTopP := defaultGenerationTopP
	if topP != nil {
		generationTopP = *topP
	}
	generationTopK := defaultGenerationTopK
	if topK != nil {
		generationTopK = *topK
	}
	opts.Config = &genai.GenerationConfig{
		Temperature: ptr(generationTemperature),
		TopP:        ptr(generationTopP),
		TopK:        ptr(generationTopK),
	}
	if outputAsJSON {
		opts.Config.ResponseMIMEType = "application/json"
	}

	logVerbose(verboseMaximum, vbs, "with generation options: %v", prettify(opts))

	// generate
	type result struct {
		exit int
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		endsWithNewLine := false

		if err := gtc.GenerateStreamed(
			ctx,
			prompt,
			files,
			func(data gt.StreamCallbackData) {
				if data.TextDelta != nil {
					fmt.Print(*data.TextDelta)

					endsWithNewLine = strings.HasSuffix(*data.TextDelta, "\n")
				} else if data.NumTokens != nil {
					if !endsWithNewLine {
						fmt.Println()
					}

					// print the number of tokens
					logVerbose(
						verboseMinimum,
						vbs,
						"input tokens: %d / output tokens: %d", data.NumTokens.Input, data.NumTokens.Output,
					)

					// success
					ch <- result{
						exit: 0,
						err:  nil,
					}
				} else if data.Error != nil {
					// error
					ch <- result{
						exit: 1,
						err:  fmt.Errorf("streaming failed: %s", gt.ErrToStr(data.Error)),
					}
				}
			},
			opts,
		); err != nil {
			// error
			ch <- result{
				exit: 1,
				err:  fmt.Errorf("generation failed: %s", gt.ErrToStr(err)),
			}
		}
	}()

	// wait for the generation to finish
	res := <-ch

	return res.exit, res.err
}

// cache context
func cacheContext(
	ctx context.Context,
	timeoutSeconds int,
	googleAIAPIKey, googleAIModel string,
	systemInstruction string,
	prompt *string, promptFiles map[string][]byte, filepaths []*string,
	cachedContextDisplayName *string,
	vbs []bool,
) (exit int, e error) {
	logVerbose(verboseMedium, vbs, "caching context...")

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	// gemini things client
	gtc, err := gt.NewClient(googleAIAPIKey, googleAIModel)
	if err != nil {
		return 1, err
	}
	defer func() {
		if err := gtc.Close(); err != nil {
			logError("Failed to close client: %s", err)
		}
	}()

	// configure gemini things client
	gtc.SetTimeout(timeoutSeconds)
	gtc.SetSystemInstructionFunc(func() string {
		return systemInstruction
	})

	// read & close files
	files, filesToClose, err := openFilesForPrompt(promptFiles, filepaths)
	if err != nil {
		return 1, err
	}
	defer func() {
		for _, toClose := range filesToClose {
			if err := toClose.Close(); err != nil {
				logError("Failed to close file: %s", err)
			}
		}
	}()

	// cache context and print the cached context's name
	if name, err := gtc.CacheContext(ctx, &systemInstruction, prompt, files, nil, nil, cachedContextDisplayName); err == nil {
		fmt.Print(name)
	} else {
		return 1, err
	}

	// success
	return 0, nil
}

// list cached contexts
func listCachedContexts(
	ctx context.Context,
	timeoutSeconds int,
	googleAIAPIKey, googleAIModel string,
	vbs []bool,
) (exit int, e error) {
	logVerbose(verboseMedium, vbs, "listing cached contexts...")

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	// gemini things client
	gtc, err := gt.NewClient(googleAIAPIKey, googleAIModel)
	if err != nil {
		return 1, err
	}
	defer func() {
		if err := gtc.Close(); err != nil {
			logError("Failed to close client: %s", err)
		}
	}()

	// configure gemini things client
	gtc.SetTimeout(timeoutSeconds)

	if listed, err := gtc.ListAllCachedContexts(ctx); err == nil {
		if len(listed) > 0 {
			fmt.Printf("%-28s  %-28s %-20s %s\n", "Name", "Model", "Expires", "Display Name")

			for _, content := range listed {
				fmt.Printf("%-28s  %-28s %-20s %s\n",
					content.Name,
					content.Model,
					content.Expiration.ExpireTime.Format("2006-01-02 15:04 MST"),
					content.DisplayName)
			}
		} else {
			return 1, fmt.Errorf("no cached contexts")
		}
	} else {
		return 1, err
	}

	// success
	return 0, nil
}

// delete cached context
func deleteCachedContext(
	ctx context.Context,
	timeoutSeconds int,
	googleAIAPIKey, googleAIModel string,
	cachedContextName string,
	vbs []bool,
) (exit int, e error) {
	logVerbose(verboseMedium, vbs, "deleting cached context...")

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	// gemini things client
	gtc, err := gt.NewClient(googleAIAPIKey, googleAIModel)
	if err != nil {
		return 1, err
	}
	defer func() {
		if err := gtc.Close(); err != nil {
			logError("Failed to close client: %s", err)
		}
	}()

	// configure gemini things client
	gtc.SetTimeout(timeoutSeconds)

	if err := gtc.DeleteCachedContext(ctx, cachedContextName); err != nil {
		return 1, err
	}

	// success
	return 0, nil
}
