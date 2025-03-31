// gemini.go
//
// things for using Gemini APIs

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"google.golang.org/genai"

	gt "github.com/meinside/gemini-things-go"
)

// generation parameter constants
//
// (https://ai.google.dev/gemini-api/docs/text-generation?lang=go#configure)
const (
	defaultGenerationTemperature = float32(1.0)
	defaultGenerationTopP        = float32(0.95)
	defaultGenerationTopK        = int32(20)

	// https://ai.google.dev/gemini-api/docs/models/gemini#text-embedding
	defaultEmbeddingsChunkSize           uint = 2048 * 2
	defaultEmbeddingsChunkOverlappedSize uint = 64
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
	generateImages, saveImagesToFiles bool,
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
		opts.CachedContent = strings.TrimSpace(*cachedContextName)
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
		TopK:        ptr(float32(generationTopK)),
	}
	if outputAsJSON {
		opts.Config.ResponseMIMEType = "application/json"
	}
	if generateImages {
		gtc.SetSystemInstructionFunc(nil)

		opts.ResponseModalities = []string{
			gt.ResponseModalityText,
			gt.ResponseModalityImage,
		}
	}
	opts.IgnoreUnsupportedType = true

	logVerbose(verboseMaximum, vbs, "with generation options: %v", prettify(opts))

	// generate
	type result struct {
		exit int
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		endsWithNewLine := false

		prompts := []gt.Prompt{gt.PromptFromText(prompt)}
		for filename, file := range files {
			prompts = append(prompts, gt.PromptFromFile(filename, file))
		}

		if err := gtc.GenerateStreamed(
			ctx,
			prompts,
			func(data gt.StreamCallbackData) {
				if data.TextDelta != nil {
					fmt.Print(*data.TextDelta)

					endsWithNewLine = strings.HasSuffix(*data.TextDelta, "\n")
				} else if data.InlineData != nil {
					if !endsWithNewLine {
						fmt.Println()
					}

					// (images)
					if strings.HasPrefix(data.InlineData.MIMEType, "image/") {
						if saveImagesToFiles {
							fpath := tempFilepath(data.InlineData.MIMEType, "image")

							logVerbose(
								verboseMedium,
								vbs,
								"saving file (%s;%d bytes) to: %s...", data.InlineData.MIMEType, len(data.InlineData.Data), fpath,
							)

							if err := os.WriteFile(fpath, data.InlineData.Data, 0640); err != nil {
								// error
								ch <- result{
									exit: 1,
									err:  fmt.Errorf("saving file failed: %s", err),
								}
							} else {
								logMessage(verboseMinimum, "Saved to file: %s", fpath)
							}
						} else {
							logVerbose(
								verboseMedium,
								vbs,
								"displaying image (%s;%d bytes) on terminal...", data.InlineData.MIMEType, len(data.InlineData.Data),
							)

							// display on terminal
							if err := displayImageOnTerminal(data.InlineData.Data, data.InlineData.MIMEType); err != nil {
								// error
								ch <- result{
									exit: 1,
									err:  fmt.Errorf("image display failed: %s", err),
								}
							} else {
								fmt.Println()
							}
						}
					} else { // TODO: NOTE: add more types here
						logError("Unsupported mime type of inline data: %s", data.InlineData.MIMEType)
					}
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
				} else if data.FinishReason != nil {
					if !endsWithNewLine {
						fmt.Println()
					}

					// print the finish reason
					logVerbose(
						verboseMinimum,
						vbs,
						"finishing with reason: %s", *data.FinishReason,
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
	select {
	case <-ctx.Done(): // timeout
		return 1, fmt.Errorf("generation timed out: %w", ctx.Err())
	case res := <-ch:
		return res.exit, res.err
	}
}

// generate embeddings with given things
func doEmbeddingsGeneration(
	ctx context.Context,
	timeoutSeconds int,
	googleAIAPIKey, googleAIEmbeddingsModel string,
	prompt string,
	chunkSize, overlappedChunkSize *uint,
	vbs []bool,
) (exit int, e error) {
	logVerbose(verboseMedium, vbs, "generating embeddings...")

	if chunkSize == nil {
		chunkSize = ptr(defaultEmbeddingsChunkSize)
	}
	if overlappedChunkSize == nil {
		overlappedChunkSize = ptr(defaultEmbeddingsChunkOverlappedSize)
	}

	// chunk prompt text
	chunks, err := gt.ChunkText(prompt, gt.TextChunkOption{
		ChunkSize:      *chunkSize,
		OverlappedSize: *overlappedChunkSize,
		EllipsesText:   "...",
	})
	if err != nil {
		return 1, fmt.Errorf("failed to chunk text: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	// gemini things client
	gtc, err := gt.NewClient(googleAIAPIKey, googleAIEmbeddingsModel)
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

	// iterate chunks and generate embeddings
	type embedding struct {
		Text    string    `json:"text"`
		Vectors []float32 `json:"vectors"`
	}
	type embeddings struct {
		Original string      `json:"original"`
		Chunks   []embedding `json:"chunks"`
	}
	embeds := embeddings{
		Original: prompt,
		Chunks:   []embedding{},
	}
	for i, text := range chunks.Chunks {
		if vectors, err := gtc.GenerateEmbeddings(ctx, "", []*genai.Content{
			genai.NewContentFromText(text, gt.RoleUser),
		}); err != nil {
			return 1, fmt.Errorf("embeddings failed for chunk[%d]: %w", i, err)
		} else {
			embeds.Chunks = append(embeds.Chunks, embedding{
				Text:    text,
				Vectors: vectors[0],
			})
		}
	}

	// print result in JSON format
	if encoded, err := json.Marshal(embeds); err != nil {
		return 1, fmt.Errorf("embeddings encoding failed: %w", err)
	} else {
		fmt.Printf("%s\n", string(encoded))

		return 0, nil
	}
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
	prompts := []gt.Prompt{gt.PromptFromText(*prompt)}
	for filename, file := range files {
		prompts = append(prompts, gt.PromptFromFile(filename, file))
	}
	if name, err := gtc.CacheContext(
		ctx,
		&systemInstruction,
		prompts,
		nil,
		nil,
		cachedContextDisplayName,
	); err == nil {
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
					content.ExpireTime.Format("2006-01-02 15:04 MST"),
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
