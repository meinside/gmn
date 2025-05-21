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

	"github.com/fatih/color"
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
	apiKey, model string,
	systemInstruction string, temperature, topP *float32, topK *int32,
	prompts []gt.Prompt, promptFiles map[string][]byte, filepaths []*string,
	withThinking bool, thinkingBudget *int32,
	withGrounding bool,
	cachedContextName *string,
	outputAsJSON bool,
	generateImages, saveImagesToFiles bool, saveImagesToDir *string,
	ignoreUnsupportedType bool,
	vbs []bool,
) (exit int, e error) {
	logVerbose(verboseMedium, vbs, "generating...")

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	// gemini things client
	gtc, err := gt.NewClient(apiKey, model)
	if err != nil {
		return 1, err
	}
	defer func() {
		if err := gtc.Close(); err != nil {
			logError("Failed to close client: %s", err)
		}
	}()

	logVerbose(verboseMaximum, vbs, "with model: %s", model)

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
	opts.ThinkingOn = withThinking
	if thinkingBudget != nil {
		opts.ThinkingBudget = *thinkingBudget
	}
	if withGrounding {
		opts.Tools = []*genai.Tool{
			{
				GoogleSearch: &genai.GoogleSearch{},
			},
		}
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

		for filename, file := range files {
			prompts = append(prompts, gt.PromptFromFile(filename, file))
		}

		// iterate generated stream
		for it, err := range gtc.GenerateStreamIterated(
			ctx,
			prompts,
			opts,
		) {
			if err == nil {
				// save token usages
				tokenUsages := []string{}
				if it.UsageMetadata != nil {
					if it.UsageMetadata.PromptTokenCount != 0 {
						tokenUsages = append(tokenUsages, fmt.Sprintf("prompt: %d", it.UsageMetadata.PromptTokenCount))
					}
					if it.UsageMetadata.CandidatesTokenCount != 0 {
						tokenUsages = append(tokenUsages, fmt.Sprintf("candidates: %d", it.UsageMetadata.CandidatesTokenCount))
					}
					if it.UsageMetadata.CachedContentTokenCount != 0 {
						tokenUsages = append(tokenUsages, fmt.Sprintf("cached: %d", it.UsageMetadata.CachedContentTokenCount))
					}
					if it.UsageMetadata.ToolUsePromptTokenCount != 0 {
						tokenUsages = append(tokenUsages, fmt.Sprintf("tool use: %d", it.UsageMetadata.ToolUsePromptTokenCount))
					}
					if it.UsageMetadata.ThoughtsTokenCount != 0 {
						tokenUsages = append(tokenUsages, fmt.Sprintf("thoughts: %d", it.UsageMetadata.ThoughtsTokenCount))
					}
					if it.UsageMetadata.TotalTokenCount != 0 {
						tokenUsages = append(tokenUsages, fmt.Sprintf("total: %d", it.UsageMetadata.TotalTokenCount))
					}
				}

				for _, cand := range it.Candidates {
					// content
					if cand.Content != nil {
						for _, part := range cand.Content.Parts {
							// FIXME: not tested
							if part.Thought {
								fmt.Print("<thought>")
							}

							if part.Text != "" {
								fmt.Print(part.Text)

								endsWithNewLine = strings.HasSuffix(part.Text, "\n")
							} else if part.InlineData != nil {
								if !endsWithNewLine { // NOTE: make sure to insert a new line before displaying an image or etc.
									fmt.Println()
								}

								// (images)
								if strings.HasPrefix(part.InlineData.MIMEType, "image/") {
									if saveImagesToFiles || saveImagesToDir != nil {
										fpath := genFilepath(
											part.InlineData.MIMEType,
											"image",
											saveImagesToDir,
										)

										logVerbose(
											verboseMedium,
											vbs,
											"saving file (%s;%d bytes) to: %s...", part.InlineData.MIMEType, len(part.InlineData.Data), fpath,
										)

										if err := os.WriteFile(fpath, part.InlineData.Data, 0640); err != nil {
											// error
											ch <- result{
												exit: 1,
												err:  fmt.Errorf("saving file failed: %s", err),
											}
											return
										} else {
											logMessage(verboseMinimum, "Saved to file: %s", fpath)

											endsWithNewLine = true
										}
									} else {
										logVerbose(
											verboseMedium,
											vbs,
											"displaying image (%s;%d bytes) on terminal...", part.InlineData.MIMEType, len(part.InlineData.Data),
										)

										// display on terminal
										if err := displayImageOnTerminal(part.InlineData.Data, part.InlineData.MIMEType); err != nil {
											// error
											ch <- result{
												exit: 1,
												err:  fmt.Errorf("image display failed: %s", err),
											}
											return
										} else { // NOTE: make sure to insert a new line after an image
											fmt.Println()

											endsWithNewLine = true
										}
									}
								} else { // TODO: NOTE: add more types here
									logError("Unsupported mime type of inline data: %s", part.InlineData.MIMEType)
								}
							} else {
								if !ignoreUnsupportedType {
									logError("Unsupported type of content part: %s", prettify(part))
								}
							}
						}
					}

					// finish reason
					if cand.FinishReason != "" {
						if !endsWithNewLine { // NOTE: make sure to insert a new line before displaying finish reason
							fmt.Println()
						}

						// print the number of tokens before priting the finish reason
						if len(tokenUsages) > 0 {
							logVerbose(
								verboseMinimum,
								vbs,
								"tokens %s", strings.Join(tokenUsages, ", "),
							)
						}

						// print the finish reason
						logVerbose(
							verboseMinimum,
							vbs,
							"finishing with reason: %s", cand.FinishReason,
						)

						// success
						ch <- result{
							exit: 0,
							err:  nil,
						}
						return
					}
				}
			} else {
				// error
				ch <- result{
					exit: 1,
					err:  fmt.Errorf("stream iteration failed: %s", gt.ErrToStr(err)),
				}
				return
			}
		}

		// finish anyway
		ch <- result{
			exit: 0,
			err:  nil,
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
	apiKey, model string,
	prompt string,
	taskType *string,
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
	gtc, err := gt.NewClient(apiKey, model)
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

	// embeddings task type
	selectedTaskType := gt.EmbeddingTaskUnspecified
	if taskType != nil {
		selectedTaskType = gt.EmbeddingTaskType(*taskType)
	}

	// iterate chunks and generate embeddings
	type embedding struct {
		Text    string    `json:"text"`
		Vectors []float32 `json:"vectors"`
	}
	type embeddings struct {
		Original string               `json:"original"`
		TaskType gt.EmbeddingTaskType `json:"taskType"`
		Chunks   []embedding          `json:"chunks"`
	}
	embeds := embeddings{
		Original: prompt,
		TaskType: selectedTaskType,
		Chunks:   []embedding{},
	}
	for i, text := range chunks.Chunks {
		if vectors, err := gtc.GenerateEmbeddings(
			ctx,
			"",
			[]*genai.Content{
				genai.NewContentFromText(text, gt.RoleUser),
			},
			&selectedTaskType,
		); err != nil {
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
	apiKey, model string,
	systemInstruction string,
	prompts []gt.Prompt, promptFiles map[string][]byte, filepaths []*string,
	cachedContextDisplayName *string,
	vbs []bool,
) (exit int, e error) {
	logVerbose(verboseMedium, vbs, "caching context...")

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	// gemini things client
	gtc, err := gt.NewClient(apiKey, model)
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
	apiKey string,
	vbs []bool,
) (exit int, e error) {
	logVerbose(verboseMedium, vbs, "listing cached contexts...")

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	// gemini things client
	gtc, err := gt.NewClient(apiKey)
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
			for _, content := range listed {
				printColored(color.FgGreen, "%s", content.Name)
				if len(content.DisplayName) > 0 {
					printColored(color.FgWhite, " (%s)", content.DisplayName)
				}
				printColored(color.FgWhite, `
  > model: %s
  > created: %s
  > expires: %s
`,
					content.Model,
					content.CreateTime.Format("2006-01-02 15:04 MST"),
					content.ExpireTime.Format("2006-01-02 15:04 MST"),
				)
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
	apiKey string,
	cachedContextName string,
	vbs []bool,
) (exit int, e error) {
	logVerbose(verboseMedium, vbs, "deleting cached context...")

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	// gemini things client
	gtc, err := gt.NewClient(apiKey)
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

// list models
func listModels(
	ctx context.Context,
	timeoutSeconds int,
	apiKey string,
	vbs []bool,
) (exit int, e error) {
	logVerbose(verboseMedium, vbs, "listing models...")

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	// gemini things client
	gtc, err := gt.NewClient(apiKey)
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

	if models, err := gtc.ListModels(ctx); err != nil {
		return 1, err
	} else {
		for _, model := range models {
			printColored(color.FgGreen, "%s", model.Name)

			printColored(color.FgWhite, ` (%s)
  > input tokens: %d
  > output tokens: %d
  > supported actions: %s
`, model.DisplayName, model.InputTokenLimit, model.OutputTokenLimit, strings.Join(model.SupportedActions, ", "))
		}
	}

	// success
	return 0, nil
}
