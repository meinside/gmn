// generation.go
//
// things for using Gemini APIs and generating files

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/gabriel-vasile/mimetype"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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
	writer *outputWriter,
	timeoutSeconds int,
	apiKey, model string,
	systemInstruction string, temperature, topP *float32, topK *int32,
	prompts []gt.Prompt, promptFiles map[string][]byte, filepaths []*string,
	withThinking bool, thinkingBudget *int32,
	withGrounding bool,
	cachedContextName *string,
	forcePrintCallbackResults bool, recurseOnCallbackResults bool, maxCallbackLoopCount int, forceCallDestructiveTools bool,
	tools []genai.Tool, toolConfig *genai.ToolConfig, toolCallbacks map[string]string, toolCallbacksConfirm map[string]bool,
	mcpConnsAndTools mcpConnectionsAndTools,
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

	writer.verbose(
		verboseMedium,
		vbs,
		"generating...",
	)

	ctx, cancel := context.WithTimeout(
		ctx,
		time.Duration(timeoutSeconds)*time.Second,
	)
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
			writer.error(
				"Failed to close client: %s",
				err,
			)
		}
	}()

	writer.verbose(
		verboseMaximum,
		vbs,
		"with model: %s",
		model,
	)

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
				writer.error(
					"Failed to close file: %s",
					err,
				)
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
	opts.Tools = []*genai.Tool{}
	// (tools and tool config)
	for _, tool := range tools {
		opts.Tools = append(opts.Tools, &tool)
	}
	if toolConfig != nil {
		opts.ToolConfig = toolConfig
	}
	var mcpToGeminiTools []*genai.FunctionDeclaration = nil
	for _, connsAndTools := range mcpConnsAndTools {
		if geminiTools, err := gt.MCPToGeminiTools(connsAndTools.tools); err == nil {
			if len(opts.Tools) > 0 {
				opts.Tools[0].FunctionDeclarations = append(opts.Tools[0].FunctionDeclarations, geminiTools...)
			} else {
				opts.Tools = append(opts.Tools, &genai.Tool{
					FunctionDeclarations: geminiTools,
				})
			}
			mcpToGeminiTools = append(mcpToGeminiTools, geminiTools...)
		} else {
			return 1, fmt.Errorf(
				"failed to convert MCP tools for gemini: %w",
				err,
			)
		}
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
				opts.SpeechConfig.MultiSpeakerVoiceConfig.SpeakerVoiceConfigs = append(
					opts.SpeechConfig.MultiSpeakerVoiceConfig.SpeakerVoiceConfigs,
					&genai.SpeakerVoiceConfig{
						Speaker: speaker,
						VoiceConfig: &genai.VoiceConfig{
							PrebuiltVoiceConfig: &genai.PrebuiltVoiceConfig{
								VoiceName: voice,
							},
						},
					},
				)
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
	opts.History = append(opts.History, pastGenerations...)

	writer.verbose(
		verboseMaximum,
		vbs,
		"with prompts: %v",
		prompts,
	)

	writer.verbose(
		verboseMaximum,
		vbs,
		"with generation options: %s",
		prettify(opts),
	)

	// generate
	type result struct {
		exit int
		err  error
	}
	ch := make(chan result, 1)
	go func() {
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
						tokenUsages = append(tokenUsages, fmt.Sprintf(
							"prompt: %d",
							it.UsageMetadata.PromptTokenCount,
						))
					}
					if it.UsageMetadata.CandidatesTokenCount != 0 {
						tokenUsages = append(tokenUsages, fmt.Sprintf(
							"candidates: %d",
							it.UsageMetadata.CandidatesTokenCount,
						))
					}
					if it.UsageMetadata.CachedContentTokenCount != 0 {
						tokenUsages = append(tokenUsages, fmt.Sprintf(
							"cached: %d",
							it.UsageMetadata.CachedContentTokenCount,
						))
					}
					if it.UsageMetadata.ToolUsePromptTokenCount != 0 {
						tokenUsages = append(tokenUsages, fmt.Sprintf(
							"tool use: %d",
							it.UsageMetadata.ToolUsePromptTokenCount,
						))
					}
					if it.UsageMetadata.ThoughtsTokenCount != 0 {
						tokenUsages = append(tokenUsages, fmt.Sprintf(
							"thoughts: %d",
							it.UsageMetadata.ThoughtsTokenCount,
						))
					}
					if it.UsageMetadata.TotalTokenCount != 0 {
						tokenUsages = append(tokenUsages, fmt.Sprintf(
							"total: %d",
							it.UsageMetadata.TotalTokenCount,
						))
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
										writer.printColored(
											color.FgHiYellow,
											"<thought>\n",
										)

										thoughtBegan, thoughtEnded = true, false
										isThinking = true
									}
								} else {
									if thoughtBegan {
										thoughtBegan = false

										if !thoughtEnded {
											writer.printColored(
												color.FgHiYellow,
												"</thought>\n",
											)

											thoughtEnded = true
											isThinking = false
										}
									}
								}
							}

							if part.Text != "" {
								if isThinking {
									writer.printColored(
										color.FgHiYellow,
										"%s",
										part.Text,
									)
								} else {
									writer.printColored(
										color.FgHiWhite,
										"%s",
										part.Text,
									)

									// NOTE: ignore thoughts from model
									bufModelResponse.WriteString(part.Text)
								}
							} else if part.InlineData != nil {
								// flush model response
								pastGenerations = appendAndFlushModelResponse(pastGenerations, bufModelResponse)

								writer.makeSureToEndWithNewLine()

								if strings.HasPrefix(part.InlineData.MIMEType, "image/") { // (images)
									if saveImagesToFiles || saveImagesToDir != nil {
										fpath := genFilepath(
											part.InlineData.MIMEType,
											"image",
											saveImagesToDir,
										)

										writer.verbose(
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
											writer.print(
												verboseMinimum,
												"Saved image to file: %s",
												fpath,
											)
										}
									} else {
										writer.verbose(
											verboseMedium,
											vbs,
											"displaying image (%s;%d bytes) on terminal...",
											part.InlineData.MIMEType,
											len(part.InlineData.Data),
										)

										// display on terminal
										if err := displayImageOnTerminal(
											part.InlineData.Data,
											part.InlineData.MIMEType,
										); err != nil {
											// error
											ch <- result{
												exit: 1,
												err:  fmt.Errorf("image display failed: %s", err),
											}
											return
										} else { // NOTE: make sure to insert a new line after an image
											writer.println()
										}
									}
								} else if strings.HasPrefix(part.InlineData.MIMEType, "audio/") { // (audio)
									// check codec and birtate
									var speechCodec string
									var bitRate int
									for split := range strings.SplitSeq(part.InlineData.MIMEType, ";") {
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

											writer.verbose(
												verboseMedium,
												vbs,
												"saving file (%s;%d bytes) to: %s...",
												mimeType,
												len(converted),
												fpath,
											)

											if err := os.WriteFile(
												fpath,
												converted,
												0640,
											); err != nil {
												// error
												ch <- result{
													exit: 1,
													err:  fmt.Errorf("saving file failed: %s", err),
												}
												return
											} else {
												writer.print(
													verboseMinimum,
													"Saved speech to file: %s",
													fpath,
												)
											}
										} else {
											// error
											ch <- result{
												exit: 1,
												err: fmt.Errorf(
													"failed to convert speech from %s to wav: %w",
													speechCodec,
													err,
												),
											}
											return
										}
									} else {
										// error
										ch <- result{
											exit: 1,
											err: fmt.Errorf(
												"unsupported speech with codec: %s and bitrate: %d",
												speechCodec,
												bitRate,
											),
										}
										return
									}
								} else { // TODO: NOTE: add more types here
									writer.error(
										"Unsupported mime type of inline data: %s",
										part.InlineData.MIMEType,
									)
								}
							} else if part.FunctionCall != nil {
								// flush model response
								pastGenerations = appendAndFlushModelResponse(pastGenerations, bufModelResponse)

								// string representation of function and its arguments
								fn := fmt.Sprintf(
									`%s(%s)`,
									part.FunctionCall.Name,
									prettify(part.FunctionCall.Args, true),
								)

								// NOTE: check if past generations has duplicated `fn` (for avoiding infinite loop)
								duplicated := 0
								for _, past := range pastGenerations {
									for _, part := range past.Parts {
										if strings.Contains(part.Text, fn) {
											duplicated++
										}
									}
								}
								if duplicated > maxCallbackLoopCount {
									// error
									ch <- result{
										exit: 1,
										err: fmt.Errorf(
											"possible infinite loop of function call detected (permitted max count: %d): '%s'",
											maxCallbackLoopCount,
											fn,
										),
									}
									return
								}

								// NOTE: if tool callbackPath exists for this function call, execute it with the args
								if callbackPath, exists := toolCallbacks[part.FunctionCall.Name]; exists {
									fnCallback, okToRun := checkCallbackPath(
										callbackPath,
										toolCallbacksConfirm,
										forceCallDestructiveTools,
										part.FunctionCall,
										writer,
										vbs,
									)

									if okToRun {
										writer.verbose(
											verboseMedium,
											vbs,
											"executing callback...",
										)

										if res, err := fnCallback(); err != nil {
											// error
											ch <- result{
												exit: 1,
												err: fmt.Errorf(
													"tool callback failed: %s",
													err,
												),
											}
											return
										} else {
											// warn that there are tool callbacks ignored
											if len(toolCallbacks) > 0 && !recurseOnCallbackResults {
												writer.warn(
													"Not recursing, ignoring the result of '%s'.",
													fn,
												)
											}

											// print the result of execution
											if forcePrintCallbackResults ||
												verboseLevel(vbs) >= verboseMinimum {
												writer.printColored(
													color.FgHiCyan,
													"%s\n",
													res,
												)
											}

											// flush model response
											pastGenerations = appendAndFlushModelResponse(pastGenerations, bufModelResponse)

											// append function call result
											pastGenerations = append(pastGenerations, genai.Content{
												Role: "user",
												Parts: []*genai.Part{
													{
														Text: fmt.Sprintf(
															`Result of function '%s':

%s`,
															fn,
															res,
														),
													},
												},
											})
										}
									} else {
										writer.printColored(
											color.FgHiYellow,
											"Skipped execution of callback '%s' for function '%s'.\n",
											callbackPath,
											fn,
										)

										// flush model response
										pastGenerations = appendAndFlushModelResponse(pastGenerations, bufModelResponse)

										// append function call result (not called)
										pastGenerations = append(pastGenerations, genai.Content{
											Role: "user",
											Parts: []*genai.Part{
												{
													Text: fmt.Sprintf(
														`User chose not to call function '%s'.`,
														fn,
													),
												},
											},
										})
									}
								} else if mcpToGeminiTools != nil {
									// if there is a matching tool,
									if slices.ContainsFunc(mcpToGeminiTools, func(tool *genai.FunctionDeclaration) bool {
										return tool.Name == part.FunctionCall.Name
									}) {
										okToRun := false

										var serverKey string
										var serverType mcpServerType
										var mc *mcp.ClientSession
										var tool mcp.Tool
										var toolExists bool
										if serverKey, serverType, mc, tool, toolExists = mcpToolFrom(
											mcpConnsAndTools,
											part.FunctionCall.Name,
										); toolExists {
											// check if matched tool requires confirmation
											if tool.Annotations != nil &&
												tool.Annotations.DestructiveHint != nil &&
												*tool.Annotations.DestructiveHint &&
												!forceCallDestructiveTools {
												okToRun = confirm(fmt.Sprintf(
													"May I call tool '%s' from '%s' for function '%s'?",
													part.FunctionCall.Name,
													stripServerInfo(serverType, serverKey),
													fn,
												))
											} else {
												okToRun = true
											}
										} else {
											// no matching tool with given server & function name
											writer.warn(
												"No matching tool '%s' from '%s'; generated function call: %s",
												part.FunctionCall.Name,
												stripServerInfo(serverType, serverKey),
												prettify(part.FunctionCall),
											)
										}

										if okToRun {
											writer.verbose(
												verboseMedium,
												vbs,
												"calling tool '%s' from '%s'...",
												part.FunctionCall.Name,
												stripServerInfo(serverType, serverKey),
											)

											// call tool,
											if res, err := fetchMCPToolCallResult(
												ctx,
												mc,
												part.FunctionCall.Name,
												part.FunctionCall.Args,
											); err == nil {
												if generated, err := gt.MCPCallToolResultToGeminiPrompts(res); err == nil {
													// warn that there are tools ignored
													if len(mcpConnsAndTools) > 0 && !recurseOnCallbackResults {
														writer.warn(
															"Not recursing, ignoring the result of '%s'.",
															fn,
														)
													}

													// print the result of execution
													if forcePrintCallbackResults ||
														verboseLevel(vbs) >= verboseMinimum {
														for _, gen := range generated {
															writer.printColored(
																color.FgHiCyan,
																"%s\n",
																gen.String(),
															)
														}
													}

													// flush model response
													pastGenerations = appendAndFlushModelResponse(pastGenerations, bufModelResponse)

													// append function call result
													parts := []*genai.Part{
														{
															Text: fmt.Sprintf(
																`Result of function '%s':
`,
																fn,
															),
														},
													}
													for _, gen := range generated {
														parts = append(parts, ptr(gen.ToPart()))
													}
													pastGenerations = append(pastGenerations, genai.Content{
														Role:  "user",
														Parts: parts,
													})
												} else {
													// error
													ch <- result{
														exit: 1,
														err: fmt.Errorf(
															"failed to read tool call result: %s",
															err,
														),
													}
													return
												}
											} else {
												// error
												ch <- result{
													exit: 1,
													err: fmt.Errorf(
														"tool call failed: %s",
														err,
													),
												}
												return
											}
										} else {
											writer.printColored(
												color.FgHiYellow,
												"Skipped execution of tool '%s' from '%s' for function '%s'.\n",
												part.FunctionCall.Name,
												stripServerInfo(serverType, serverKey),
												fn,
											)

											// flush model response
											pastGenerations = appendAndFlushModelResponse(pastGenerations, bufModelResponse)

											// append function call result (not called)
											pastGenerations = append(pastGenerations, genai.Content{
												Role: "user",
												Parts: []*genai.Part{
													{
														Text: fmt.Sprintf(
															`User chose not to call function '%s'.`,
															fn,
														),
													},
												},
											})
										}
									} else {
										// no matching tool, just print the function call data
										writer.print(
											verboseMinimum,
											"No matching tool; generated function call: %s",
											prettify(part.FunctionCall),
										)
									}
								} else {
									// just print the function call data
									writer.print(
										verboseMinimum,
										"Generated function call: %s",
										prettify(part.FunctionCall),
									)

									// NOTE: not to recurse infinitely
									if recurseOnCallbackResults {
										writer.warn(
											"Will skip further execution of function '%s' for avoiding infinite recursion.",
											fn,
										)
										recurseOnCallbackResults = false
									}
								}
							} else {
								// flush model response
								pastGenerations = appendAndFlushModelResponse(pastGenerations, bufModelResponse)

								if !ignoreUnsupportedType {
									writer.error(
										"Unsupported type of content part: %s",
										prettify(part),
									)
								}
							}
						}
					}

					// finish reason
					if cand.FinishReason != "" {
						// flush model response
						pastGenerations = appendAndFlushModelResponse(pastGenerations, bufModelResponse)

						writer.makeSureToEndWithNewLine() // NOTE: make sure to insert a new line before displaying finish reason

						// print the number of tokens before printing the finish reason
						if len(tokenUsages) > 0 {
							writer.verbose(
								verboseMinimum,
								vbs,
								"tokens %s",
								strings.Join(tokenUsages, ", "),
							)
						}

						// print the finish reason
						writer.verbose(
							verboseMinimum,
							vbs,
							"finishing with reason: %s",
							cand.FinishReason,
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
					err: fmt.Errorf(
						"stream iteration failed: %s",
						gt.ErrToStr(err),
					),
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
		return 1, fmt.Errorf(
			"generation timed out: %w",
			ctx.Err(),
		)
	case res := <-ch:
		// check if recursion is needed
		if res.exit == 0 &&
			res.err == nil &&
			recurseOnCallbackResults &&
			historyEndsWithUsers(pastGenerations) {
			writer.verbose(
				verboseMedium,
				vbs,
				"Generating recursively with history: %s",
				prettify(pastGenerations),
			)

			// do recursion
			return doGeneration(
				ctx,
				writer,
				timeoutSeconds,
				apiKey, model,
				systemInstruction, temperature, topP, topK,
				prompts, promptFiles, filepaths,
				withThinking, thinkingBudget,
				withGrounding,
				cachedContextName,
				forcePrintCallbackResults, recurseOnCallbackResults, maxCallbackLoopCount, forceCallDestructiveTools,
				tools, toolConfig, toolCallbacks, toolCallbacksConfirm,
				mcpConnsAndTools,
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
	writer *outputWriter,
	timeoutSeconds int,
	apiKey, model string,
	prompt string,
	taskType *string,
	chunkSize, overlappedChunkSize *uint,
	vbs []bool,
) (exit int, e error) {
	writer.verbose(
		verboseMedium,
		vbs,
		"generating embeddings...",
	)

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
		return 1, fmt.Errorf(
			"failed to chunk text: %w",
			err,
		)
	}

	ctx, cancel := context.WithTimeout(
		ctx,
		time.Duration(timeoutSeconds)*time.Second,
	)
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
			writer.error("Failed to close client: %s", err)
		}
	}()

	// configure gemini things client
	gtc.SetTimeoutSeconds(timeoutSeconds)

	// embeddings task type
	var selectedTaskType gt.EmbeddingTaskType
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
			return 1, fmt.Errorf(
				"embeddings failed for chunk[%d]: %w",
				i,
				err,
			)
		} else {
			embeds.Chunks = append(embeds.Chunks, embedding{
				Text:    text,
				Vectors: vectors[0],
			})
		}
	}

	// print result in JSON format
	if encoded, err := json.Marshal(embeds); err != nil {
		return 1, fmt.Errorf(
			"embeddings encoding failed: %w",
			err,
		)
	} else {
		writer.printColored(
			color.FgHiWhite,
			"%s\n",
			string(encoded),
		)

		return 0, nil
	}
}

// cache context
func cacheContext(
	ctx context.Context,
	writer *outputWriter,
	timeoutSeconds int,
	apiKey, model string,
	systemInstruction string,
	prompts []gt.Prompt, promptFiles map[string][]byte, filepaths []*string,
	cachedContextDisplayName *string,
	vbs []bool,
) (exit int, e error) {
	writer.verbose(
		verboseMedium,
		vbs,
		"caching context...",
	)

	ctx, cancel := context.WithTimeout(
		ctx,
		time.Duration(timeoutSeconds)*time.Second,
	)
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
			writer.error(
				"Failed to close client: %s",
				err,
			)
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
				writer.error(
					"Failed to close file: %s",
					err,
				)
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
		writer.printColored(
			color.FgHiWhite,
			"%s",
			name,
		)
	} else {
		return 1, err
	}

	// success
	return 0, nil
}

// list cached contexts
func listCachedContexts(
	ctx context.Context,
	writer *outputWriter,
	timeoutSeconds int,
	apiKey string,
	vbs []bool,
) (exit int, e error) {
	writer.verbose(
		verboseMedium,
		vbs,
		"listing cached contexts...",
	)

	ctx, cancel := context.WithTimeout(
		ctx,
		time.Duration(timeoutSeconds)*time.Second,
	)
	defer cancel()

	// gemini things client
	gtc, err := gt.NewClient(apiKey)
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

	// configure gemini things client
	gtc.SetTimeoutSeconds(timeoutSeconds)

	if listed, err := gtc.ListAllCachedContexts(ctx); err == nil {
		if len(listed) > 0 {
			for _, content := range listed {
				writer.printColored(
					color.FgHiGreen,
					"%s",
					content.Name,
				)
				if len(content.DisplayName) > 0 {
					writer.printColored(
						color.FgHiWhite,
						" (%s)",
						content.DisplayName,
					)
				}
				writer.printColored(color.FgWhite, `
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
	writer *outputWriter,
	timeoutSeconds int,
	apiKey string,
	cachedContextName string,
	vbs []bool,
) (exit int, e error) {
	writer.verbose(
		verboseMedium,
		vbs,
		"deleting cached context...",
	)

	ctx, cancel := context.WithTimeout(
		ctx,
		time.Duration(timeoutSeconds)*time.Second,
	)
	defer cancel()

	// gemini things client
	gtc, err := gt.NewClient(apiKey)
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
	writer *outputWriter,
	timeoutSeconds int,
	apiKey string,
	vbs []bool,
) (exit int, e error) {
	writer.verbose(
		verboseMedium,
		vbs,
		"listing models...",
	)

	ctx, cancel := context.WithTimeout(
		ctx,
		time.Duration(timeoutSeconds)*time.Second,
	)
	defer cancel()

	// gemini things client
	gtc, err := gt.NewClient(apiKey)
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

	// configure gemini things client
	gtc.SetTimeoutSeconds(timeoutSeconds)

	if models, err := gtc.ListModels(ctx); err != nil {
		return 1, err
	} else {
		for _, model := range models {
			writer.printColored(
				color.FgHiGreen,
				"%s",
				model.Name,
			)
			writer.printColored(
				color.FgHiWhite,
				` (%s)`,
				model.DisplayName,
			)

			writer.printColored(color.FgWhite, `
  > input tokens: %d
  > output tokens: %d
  > supported actions: %s
`, model.InputTokenLimit,
				model.OutputTokenLimit,
				strings.Join(model.SupportedActions, ", "),
			)
		}
	}

	// success
	return 0, nil
}

// append and flush model response
func appendAndFlushModelResponse(
	generatedConversations []genai.Content,
	buffer *strings.Builder,
) []genai.Content {
	if buffer.Len() > 0 {
		if len(generatedConversations) > 0 && generatedConversations[len(generatedConversations)-1].Role == "model" {
			lastContent := generatedConversations[len(generatedConversations)-1]

			// append text to the last model response
			hasTextPrompt := false
			for _, part := range slices.Backward(lastContent.Parts) {
				if part.Text != "" {
					part.Text = part.Text + buffer.String()
					hasTextPrompt = true
					break
				}
			}
			// or just append a new text part to the last model response
			if !hasTextPrompt {
				lastContent.Parts = append(lastContent.Parts, &genai.Part{
					Text: buffer.String(),
				})
			}
		} else {
			// or just append a new model response
			generatedConversations = append(generatedConversations, genai.Content{
				Role: "model",
				Parts: []*genai.Part{
					{
						Text: buffer.String(),
					},
				},
			})
		}

		// reset buffer
		buffer.Reset()
	}

	return generatedConversations
}

// predefined callback function names
const (
	fnCallbackStdin     = `@stdin`
	fnCallbackFormatter = `@format`
)

// check if given `callbackPath` is executable
func checkCallbackPath(
	callbackPath string,
	confirmToolCallbacks map[string]bool,
	forceCallDestructiveTools bool,
	fnCall *genai.FunctionCall,
	writer *outputWriter,
	vbs []bool,
) (
	fnCallback func() (string, error),
	okToRun bool,
) {
	// check if `callbackPath` is a predefined callback
	if callbackPath == fnCallbackStdin { // @stdin
		okToRun = true

		fnCallback = func() (string, error) {
			prompt := fmt.Sprintf(
				"Type your answer for function '%s(%s)'",
				fnCall.Name,
				prettify(fnCall.Args, true),
			)

			return readFromStdin(prompt)
		}
	} else if strings.HasPrefix(callbackPath, fnCallbackFormatter) { // @format
		okToRun = true

		fnCallback = func() (string, error) {
			if tpl, exists := strings.CutPrefix(callbackPath, fnCallbackFormatter+"="); exists {
				if t, err := template.New("fnFormatter").Parse(tpl); err == nil {
					buf := new(bytes.Buffer)
					if err := t.Execute(buf, fnCall.Args); err == nil {
						return buf.String(), nil
					} else {
						return "", fmt.Errorf("failed to execute template for %s: %w", fnCallbackFormatter, err)
					}
				} else {
					return "", fmt.Errorf("failed to parse format for %s: %w", fnCallbackFormatter, err)
				}
			} else {
				if marshalled, err := json.MarshalIndent(fnCall.Args, "", "  "); err == nil {
					return string(marshalled), nil
				} else {
					return "", fmt.Errorf("failed to marshal to JSON for %s: %w", fnCallbackFormatter, err)
				}
			}
		}
	} else { // ordinary path of binary/script:
		// ask for confirmation
		if confirmNeeded, exists := confirmToolCallbacks[fnCall.Name]; exists && confirmNeeded && !forceCallDestructiveTools {
			okToRun = confirm(fmt.Sprintf(
				"May I execute callback '%s' for function '%s(%s)'?",
				callbackPath,
				fnCall.Name,
				prettify(fnCall.Args, true),
			))
		} else {
			okToRun = true
		}

		// run executable
		fnCallback = func() (string, error) {
			writer.verbose(
				verboseMinimum,
				vbs,
				"executing callback '%s' for function '%s(%s)'...",
				callbackPath,
				fnCall.Name,
				prettify(fnCall.Args, true),
			)

			return runExecutable(callbackPath, fnCall.Args)
		}
	}

	return
}
