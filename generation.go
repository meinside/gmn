// generation.go
//
// things for using Gemini APIs and generating files

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/gabriel-vasile/mimetype"
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

// wav parameter constants
const (
	wavBitDepth    = 16
	wavNumChannels = 1
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
	tools []genai.Tool, toolConfig *genai.ToolConfig, toolCallbacks map[string]string, toolCallbacksConfirm map[string]bool, recurseOnCallbackResults bool,
	outputAsJSON bool,
	generateImages, saveImagesToFiles bool, saveImagesToDir *string,
	generateSpeech bool, speechLanguage, speechVoice *string, speechVoices map[string]string, saveSpeechToDir *string,
	pastGenerations []genai.Content,
	ignoreUnsupportedType bool,
	vbs []bool,
) (exit int, e error) {
	// check params here
	if generateImages && generateSpeech {
		return 1, fmt.Errorf("cannot generate images and speech at the same time")
	}

	logVerbose(verboseMedium, vbs, "generating...")

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	// gemini things client
	gtc, err := gt.NewClient(
		apiKey,
		gt.WithModel(model),
	)
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
	gtc.SetTimeoutSeconds(timeoutSeconds)
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
	// (cached context)
	if cachedContextName != nil {
		opts.CachedContent = strings.TrimSpace(*cachedContextName)
	}
	// (temperature)
	generationTemperature := defaultGenerationTemperature
	if temperature != nil {
		generationTemperature = *temperature
	}
	// (topP)
	generationTopP := defaultGenerationTopP
	if topP != nil {
		generationTopP = *topP
	}
	// (topK)
	generationTopK := defaultGenerationTopK
	if topK != nil {
		generationTopK = *topK
	}
	opts.Config = &genai.GenerationConfig{
		Temperature: ptr(generationTemperature),
		TopP:        ptr(generationTopP),
		TopK:        ptr(float32(generationTopK)),
	}
	// (tools and tool config)
	if tools != nil {
		opts.Tools = []*genai.Tool{}
		for _, tool := range tools {
			opts.Tools = append(opts.Tools, &tool)
		}
	}
	if toolConfig != nil {
		opts.ToolConfig = toolConfig
	}
	// (JSON output)
	if outputAsJSON {
		opts.Config.ResponseMIMEType = "application/json"
	}
	// (images generation)
	if generateImages {
		gtc.SetSystemInstructionFunc(nil)

		opts.ResponseModalities = []genai.Modality{
			genai.ModalityText,
			genai.ModalityImage,
		}
	} else if generateSpeech { // (speech generation)
		gtc.SetSystemInstructionFunc(nil)

		opts.ResponseModalities = []genai.Modality{
			genai.ModalityAudio,
		}

		opts.SpeechConfig = &genai.SpeechConfig{}

		// speech language
		if speechLanguage != nil {
			opts.SpeechConfig.LanguageCode = *speechLanguage
		}

		// speech voice(s)
		if speechVoice != nil {
			opts.SpeechConfig.VoiceConfig = &genai.VoiceConfig{
				PrebuiltVoiceConfig: &genai.PrebuiltVoiceConfig{
					VoiceName: *speechVoice,
				},
			}
		} else if len(speechVoices) > 0 {
			opts.SpeechConfig.MultiSpeakerVoiceConfig = &genai.MultiSpeakerVoiceConfig{
				SpeakerVoiceConfigs: []*genai.SpeakerVoiceConfig{},
			}
			for speaker, voice := range speechVoices {
				opts.SpeechConfig.MultiSpeakerVoiceConfig.SpeakerVoiceConfigs = append(opts.SpeechConfig.MultiSpeakerVoiceConfig.SpeakerVoiceConfigs, &genai.SpeakerVoiceConfig{
					Speaker: speaker,
					VoiceConfig: &genai.VoiceConfig{
						PrebuiltVoiceConfig: &genai.PrebuiltVoiceConfig{
							VoiceName: voice,
						},
					},
				})
			}
		}
	}
	// (thinking)
	opts.ThinkingOn = withThinking
	if thinkingBudget != nil {
		opts.ThinkingBudget = *thinkingBudget
	}
	// (grounding)
	if withGrounding {
		opts.Tools = []*genai.Tool{
			{
				GoogleSearch: &genai.GoogleSearch{},
			},
		}
	}
	// (history)
	if pastGenerations == nil {
		pastGenerations = []genai.Content{}
	}
	opts.History = pastGenerations

	logVerbose(
		verboseMaximum,
		vbs,
		"with generation options: %v",
		prettify(opts),
	)

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

		// for marking <thought></thought>
		thoughtBegan, thoughtEnded := false, false
		isThinking := false

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

				// string buffer for model responses
				bufModelResponse := new(strings.Builder)

				for _, cand := range it.Candidates {
					// content
					if cand.Content != nil {
						for _, part := range cand.Content.Parts {
							// marking begin/end of thoughts
							if withThinking {
								if part.Thought {
									if !thoughtBegan {
										printColored(color.FgYellow, "<thought>\n")

										thoughtBegan, thoughtEnded = true, false
										isThinking = true
									}
								} else {
									if thoughtBegan {
										thoughtBegan = false

										if !thoughtEnded {
											printColored(color.FgYellow, "</thought>\n")

											thoughtEnded = true
											isThinking = false
										}
									}
								}
							}

							if part.Text != "" {
								if isThinking {
									printColored(color.FgYellow, part.Text)
								} else {
									printColored(color.FgWhite, part.Text)

									// NOTE: ignore thoughts from model
									bufModelResponse.WriteString(part.Text)
								}

								endsWithNewLine = strings.HasSuffix(part.Text, "\n")
							} else if part.InlineData != nil {
								// flush model response
								pastGenerations = appendAndFlushModelResponse(pastGenerations, bufModelResponse)

								if !endsWithNewLine { // NOTE: make sure to insert a new line before displaying an image or etc.
									fmt.Println()
								}

								if strings.HasPrefix(part.InlineData.MIMEType, "image/") { // (images)
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
											logMessage(verboseMinimum, "Saved image to file: %s", fpath)

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
								} else if strings.HasPrefix(part.InlineData.MIMEType, "audio/") { // (audio)
									// check codec and birtate
									var speechCodec string
									var bitRate int
									for _, split := range strings.Split(part.InlineData.MIMEType, ";") {
										if strings.HasPrefix(split, "codec=") {
											speechCodec = split[6:]
										} else if strings.HasPrefix(split, "rate=") {
											bitRate, _ = strconv.Atoi(split[5:])
										}
									}

									// convert,
									if speechCodec == "pcm" && bitRate > 0 { // FIXME: only 'pcm' is supported for now
										if converted, err := pcmToWav(
											part.InlineData.Data,
											bitRate,
											wavBitDepth,
											wavNumChannels,
										); err == nil {
											// and save file
											mimeType := mimetype.Detect(converted).String()
											fpath := genFilepath(
												mimeType,
												"audio",
												saveSpeechToDir,
											)

											logVerbose(
												verboseMedium,
												vbs,
												"saving file (%s;%d bytes) to: %s...", mimeType, len(converted), fpath,
											)

											if err := os.WriteFile(fpath, converted, 0640); err != nil {
												// error
												ch <- result{
													exit: 1,
													err:  fmt.Errorf("saving file failed: %s", err),
												}
												return
											} else {
												logMessage(verboseMinimum, "Saved speech to file: %s", fpath)

												endsWithNewLine = true
											}
										} else {
											// error
											ch <- result{
												exit: 1,
												err:  fmt.Errorf("failed to convert speech from %s to wav: %w", speechCodec, err),
											}
											return
										}
									} else {
										// error
										ch <- result{
											exit: 1,
											err:  fmt.Errorf("unsupported speech with codec: %s and bitrate: %d", speechCodec, bitRate),
										}
										return
									}
								} else { // TODO: NOTE: add more types here
									logError("Unsupported mime type of inline data: %s", part.InlineData.MIMEType)
								}
							} else if part.FunctionCall != nil {
								// flush model response
								pastGenerations = appendAndFlushModelResponse(pastGenerations, bufModelResponse)

								// append user's prompt + function call to the past generations
								pastGenerations = append(pastGenerations,
									genai.Content{
										Role: "user",
										Parts: []*genai.Part{
											{
												Text: latestTextPrompt(prompts),
											},
										},
									},
									genai.Content{
										Role: "model",
										Parts: []*genai.Part{
											{
												Text: fmt.Sprintf("Please provide the result of function: '%s'", part.FunctionCall.Name),
											},
										},
									},
								)

								// NOTE: if tool callbackPath exists for this function call, execute it with the args
								if callbackPath, exists := toolCallbacks[part.FunctionCall.Name]; exists {
									okToRun := false

									// ask for confirmation
									if confirmNeeded, exists := toolCallbacksConfirm[part.FunctionCall.Name]; exists && confirmNeeded {
										okToRun = confirm(fmt.Sprintf(
											"Run callback '%s' for function '%s' with data: %s?",
											callbackPath,
											part.FunctionCall.Name,
											prettify(part.FunctionCall.Args, true),
										))
									} else {
										okToRun = true
									}

									if okToRun {
										logVerbose(
											verboseMinimum,
											vbs,
											"executing callback '%s' for function '%s' with data %s...",
											callbackPath,
											part.FunctionCall.Name,
											prettify(part.FunctionCall.Args, true),
										)

										if res, err := runExecutable(callbackPath, part.FunctionCall.Args); err != nil {
											// error
											ch <- result{
												exit: 1,
												err:  fmt.Errorf("tool callback failed: %s", err),
											}
											return
										} else {
											// when not in mode == "AUTO", show the execution of callback
											if toolConfig.FunctionCallingConfig != nil {
												if toolConfig.FunctionCallingConfig.Mode != "AUTO" {
													printColored(
														color.FgGreen,
														"Executed callback '%s' for function '%s'.\n",
														callbackPath,
														part.FunctionCall.Name,
													)

													endsWithNewLine = true
												}
											}

											// print the result of execution
											if vb := verboseLevel(vbs); vb >= verboseMinimum {
												printColored(
													color.FgCyan,
													"%s",
													res,
												)

												endsWithNewLine = strings.HasSuffix(res, "\n")
											}

											// flush model response
											pastGenerations = appendAndFlushModelResponse(pastGenerations, bufModelResponse)

											// append function call result
											pastGenerations = append(pastGenerations, genai.Content{
												Role: "user",
												Parts: []*genai.Part{
													{
														Text: fmt.Sprintf("Result of function '%s':\n\n%s", part.FunctionCall.Name, res),
													},
												},
											})
										}
									} else {
										printColored(
											color.FgYellow,
											"Skipped execution of callback '%s' for function '%s'\n",
											callbackPath, part.FunctionCall.Name,
										)

										endsWithNewLine = true

										// flush model response
										pastGenerations = appendAndFlushModelResponse(pastGenerations, bufModelResponse)

										// append function call result (not called)
										pastGenerations = append(pastGenerations, genai.Content{
											Role: "user",
											Parts: []*genai.Part{
												{
													Text: fmt.Sprintf("User chose not to call function '%s'.", part.FunctionCall.Name),
												},
											},
										})
									}
								} else {
									// just print the function call data
									logMessage(
										verboseMinimum,
										"Function call: %s",
										prettify(part.FunctionCall),
									)

									endsWithNewLine = true
								}
							} else {
								// flush model response
								pastGenerations = appendAndFlushModelResponse(pastGenerations, bufModelResponse)

								if !ignoreUnsupportedType {
									logError("Unsupported type of content part: %s", prettify(part))

									endsWithNewLine = true
								}
							}
						}
					}

					// finish reason
					if cand.FinishReason != "" {
						// print the number of tokens before printing the finish reason
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

				// flush model response
				pastGenerations = appendAndFlushModelResponse(pastGenerations, bufModelResponse)
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
		// check if recursion is needed
		if res.exit == 0 &&
			res.err == nil &&
			recurseOnCallbackResults &&
			historyEndsWithUsers(pastGenerations) {
			logVerbose(
				verboseMedium,
				vbs,
				"Generating recursively with history: %s",
				prettify(pastGenerations),
			)

			return doGeneration(
				ctx,
				timeoutSeconds,
				apiKey, model,
				systemInstruction, temperature, topP, topK,
				[]gt.Prompt{
					gt.PromptFromText(latestTextPrompt(prompts)),
				}, nil, nil,
				withThinking, thinkingBudget,
				withGrounding,
				cachedContextName,
				tools, toolConfig, toolCallbacks, toolCallbacksConfirm, recurseOnCallbackResults,
				outputAsJSON,
				generateImages, saveImagesToFiles, saveImagesToDir,
				generateSpeech, speechLanguage, speechVoice, speechVoices, saveSpeechToDir,
				pastGenerations,
				ignoreUnsupportedType,
				vbs,
			)
		}
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
	gtc, err := gt.NewClient(
		apiKey,
		gt.WithModel(model),
	)
	if err != nil {
		return 1, err
	}
	defer func() {
		if err := gtc.Close(); err != nil {
			logError("Failed to close client: %s", err)
		}
	}()

	// configure gemini things client
	gtc.SetTimeoutSeconds(timeoutSeconds)

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
	gtc, err := gt.NewClient(
		apiKey,
		gt.WithModel(model),
	)
	if err != nil {
		return 1, err
	}
	defer func() {
		if err := gtc.Close(); err != nil {
			logError("Failed to close client: %s", err)
		}
	}()

	// configure gemini things client
	gtc.SetTimeoutSeconds(timeoutSeconds)
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
	gtc.SetTimeoutSeconds(timeoutSeconds)

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
	gtc.SetTimeoutSeconds(timeoutSeconds)

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
	gtc.SetTimeoutSeconds(timeoutSeconds)

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

func appendAndFlushModelResponse(generatedConversations []genai.Content, buffer *strings.Builder) []genai.Content {
	if buffer.Len() > 0 {
		generatedConversations = append(generatedConversations, genai.Content{
			Role: "model",
			Parts: []*genai.Part{
				{
					Text: buffer.String(),
				},
			},
		})
		buffer.Reset()
	}

	return generatedConversations
}
