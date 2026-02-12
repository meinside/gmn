// generation.go
//
// Things for generations using Gemini APIs.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"github.com/fatih/color"
	"github.com/gabriel-vasile/mimetype"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/genai"

	gt "github.com/meinside/gemini-things-go"
)

// generate text with given things
func doGeneration(
	ctx context.Context,
	writer outputWriter,
	timeoutSeconds int,
	gtc *gt.Client,
	pastGenerations []genai.Content,
	prompts []gt.Prompt, promptFiles map[string][]byte,
	tools []genai.Tool, toolConfig *genai.ToolConfig, mcpConnsAndTools mcpConnectionsAndTools, thoughtSignature []byte,
	p params,
) (exit int, e error) {
	systemInstruction := *p.Generation.DetailedOptions.SystemInstruction
	temperature := p.Generation.DetailedOptions.Temperature
	topP := p.Generation.DetailedOptions.TopP
	topK := p.Generation.DetailedOptions.TopK
	seed := p.Generation.DetailedOptions.Seed
	filepaths := p.Generation.Filepaths
	overrideMimeTypeForExt := p.OverrideFileMIMEType
	withThinking := p.Generation.ThinkingOn
	thinkingLevel := p.Generation.DetailedOptions.ThinkingLevel
	showThinking := p.Generation.DetailedOptions.ShowThinking
	withGrounding := p.Generation.GroundingOn
	withGoogleMaps := p.Generation.GoogleMaps.WithGoogleMaps
	googleMapsLatitude := p.Generation.GoogleMaps.Latitude
	googleMapsLongitude := p.Generation.GoogleMaps.Longitude
	cachedContextName := p.Caching.CachedContextName
	forcePrintCallbackResults := p.Tools.ShowCallbackResults
	recurseOnCallbackResults := p.Tools.RecurseOnCallbackResults
	maxCallbackLoopCount := p.Tools.MaxCallbackLoopCount
	forceCallDestructiveTools := p.Tools.ForceCallDestructiveTools
	toolCallbacks := p.LocalTools.ToolCallbacks
	toolCallbacksConfirm := p.LocalTools.ToolCallbacksConfirm
	outputAsJSON := p.Generation.OutputAsJSON
	generateImages := p.Generation.Image.GenerateImages
	saveImagesToFiles := p.Generation.Image.SaveToFiles
	saveImagesToDir := p.Generation.Image.SaveToDir
	generateVideos := p.Generation.Video.GenerateVideos
	negativePromptForVideo := p.Generation.Video.NegativePrompt
	resolutionForVideo := p.Generation.Video.Resolution
	referenceImagesForVideo := p.Generation.Video.ReferenceImages
	saveVideosToDir := p.Generation.Video.SaveToDir
	numVideos := p.Generation.Video.NumGenerated
	videoDurationSeconds := p.Generation.Video.DurationSeconds
	videoFPS := p.Generation.Video.FPS
	generateSpeech := p.Generation.Speech.GenerateSpeech
	speechLanguage := p.Generation.Speech.Language
	speechVoice := p.Generation.Speech.Voice
	speechVoices := p.Generation.Speech.Voices
	saveSpeechToDir := p.Generation.Speech.SaveToDir
	ignoreUnsupportedType := !p.ErrorOnUnsupportedType
	vbs := p.Verbose

	writer.verbose(
		verboseMedium,
		vbs,
		"generating...",
	)

	// configure gemini things client
	gtc.SetSystemInstructionFunc(func() string {
		return systemInstruction
	})

	// read & close files
	files, err := openFilesForPrompt(promptFiles, filepaths)
	if err != nil {
		return 1, err
	}
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

	// generation options
	opts := genai.GenerateContentConfig{}
	// (safety settings)
	opts.SafetySettings = safetySettings(gtc.Type)
	// (cached context)
	if cachedContextName != nil {
		opts.CachedContent = strings.TrimSpace(*cachedContextName)
	}
	// (temperature)
	opts.Temperature = temperature
	// (topP)
	opts.TopP = topP
	// (topK)
	if topK != nil {
		opts.TopK = new(float32(*topK))
	}
	// (seed)
	if seed != nil {
		opts.Seed = seed
	}
	// (tools and tool config)
	opts.Tools = []*genai.Tool{}
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
				last := len(opts.Tools) - 1
				if len(opts.Tools[last].FunctionDeclarations) > 0 {
					opts.Tools[last].FunctionDeclarations = append(opts.Tools[last].FunctionDeclarations, geminiTools...)
				} else {
					opts.Tools = append(opts.Tools, &genai.Tool{
						FunctionDeclarations: geminiTools,
					})
				}
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
		opts.ResponseMIMEType = "application/json"
	}
	// (images generation)
	if generateImages {
		gtc.SetSystemInstructionFunc(nil)

		opts.ResponseModalities = []string{
			string(genai.ModalityText),
			string(genai.ModalityImage),
		}
	} else if generateVideos {
		gtc.SetSystemInstructionFunc(nil)

		opts.ResponseModalities = []string{string(genai.MediaModalityVideo)}
	} else if generateSpeech { // (speech generation)
		gtc.SetSystemInstructionFunc(nil)

		opts.ResponseModalities = []string{
			string(genai.ModalityAudio),
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
	opts.ThinkingConfig = &genai.ThinkingConfig{
		IncludeThoughts: withThinking,
	}
	if thinkingLevel != nil {
		var level genai.ThinkingLevel
		switch *thinkingLevel {
		case "low":
			level = genai.ThinkingLevelLow
		case "medium":
			level = genai.ThinkingLevelMedium
		case "high":
			level = genai.ThinkingLevelHigh
		case "minimal":
			level = genai.ThinkingLevelMinimal
		default:
			level = genai.ThinkingLevelUnspecified
		}
		opts.ThinkingConfig.ThinkingLevel = level
	}
	// (grounding)
	if withGrounding {
		opts.Tools = append(opts.Tools, &genai.Tool{
			GoogleSearch: &genai.GoogleSearch{},
		})
	}
	// (google maps)
	if withGoogleMaps {
		opts.Tools = append(opts.Tools, &genai.Tool{
			GoogleMaps: &genai.GoogleMaps{},
		})
		if googleMapsLatitude != nil && googleMapsLongitude != nil {
			if opts.ToolConfig == nil {
				opts.ToolConfig = &genai.ToolConfig{}
			}
			opts.ToolConfig.RetrievalConfig = &genai.RetrievalConfig{
				LatLng: &genai.LatLng{
					Latitude:  googleMapsLatitude,
					Longitude: googleMapsLongitude,
				},
			}
		}
	}

	writer.verbose(
		verboseMaximum,
		vbs,
		"with prompts: %s",
		prettify(prompts),
	)

	writer.verbose(
		verboseMaximum,
		vbs,
		"with past generations: %s",
		prettify(pastGenerations),
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
		for _, file := range files {
			var prompt gt.Prompt
			if override, exists := overrideMimeTypeForExt[filepath.Ext(file.filepath)]; exists {
				prompt = gt.PromptFromFile(file.filename, file.reader, override) // force MIME type
			} else {
				prompt = gt.PromptFromFile(file.filename, file.reader)
			}
			prompts = append(prompts, prompt)
		}

		// for marking <thought></thought>
		thoughtBegan, thoughtEnded := false, false
		isThinking := false

		ctxContents, cancelContents := context.WithTimeout(
			ctx,
			time.Duration(timeoutSeconds)*time.Second,
		)
		defer cancelContents()

		if contentsForGeneration, err := gtc.PromptsToContents(
			ctxContents,
			prompts,
			pastGenerations,
		); err == nil {
			ctxGenerate, cancelGenerate := context.WithTimeout(
				ctx,
				time.Duration(timeoutSeconds)*time.Second,
			)
			defer cancelGenerate()

			if generateVideos {
				// read & close files
				files, err := openFilesForPrompt(nil, filepaths)
				if err != nil {
					ch <- result{
						exit: 1,
						err:  fmt.Errorf("failed to open files for video generation: %w", err),
					}
					return
				}
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

				// convert prompts to images and videos
				var prompt *string
				var firstFrame, lastFrame *genai.Image
				var videoForExtension *genai.Video
				firstFrame, lastFrame, videoForExtension, err = promptImageOrVideoFromPrompts(writer, files)
				if err != nil {
					ch <- result{
						exit: 1,
						err:  fmt.Errorf("failed to open files for video generation: %w", err),
					}
					return
				}
				if textPrompt := firstTextPrompt(prompts); textPrompt != nil {
					prompt = &textPrompt.Text
				}

				// convert reference images
				var referenceImages []*genai.VideoGenerationReferenceImage
				if referenceImages, err = convertReferenceImagesForVideoGeneration(ctxGenerate, gtc, referenceImagesForVideo); err != nil {
					ch <- result{
						exit: 1,
						err:  fmt.Errorf("failed to convert reference images for video generation: %w", err),
					}
					return
				}

				options := &genai.GenerateVideosConfig{
					NumberOfVideos:   numVideos,
					DurationSeconds:  new(videoDurationSeconds),
					FPS:              new(videoFPS),
					EnhancePrompt:    true,
					PersonGeneration: "allow_adult",
				}
				if lastFrame != nil {
					options.LastFrame = lastFrame
				}
				if len(referenceImages) > 0 {
					options.ReferenceImages = referenceImages
				}
				if negativePromptForVideo != nil {
					options.NegativePrompt = *negativePromptForVideo
				}
				if resolutionForVideo == nil {
					resolutionForVideo = new(defaultGeneratedVideosResolution)
				}
				options.Resolution = *resolutionForVideo

				if res, err := gtc.GenerateVideos(
					ctxGenerate,
					prompt,
					firstFrame,
					videoForExtension,
					options,
				); err == nil {
					for _, video := range res.GeneratedVideos {
						var data []byte
						var mimeType string
						if len(video.Video.VideoBytes) > 0 {
							data = video.Video.VideoBytes
							// mimeType = mimetype.Detect(data).String()
							mimeType = video.Video.MIMEType
						} else if len(video.Video.URI) > 0 {
							var ferr error
							if obj := gtc.Storage().Bucket(gtc.GetBucketName()).Object(video.Video.URI); obj == nil {
								var reader *storage.Reader
								if reader, ferr = obj.NewReader(ctxGenerate); ferr == nil {
									defer func() { _ = reader.Close() }()

									if data, ferr = io.ReadAll(reader); ferr == nil {
										mimeType = video.Video.MIMEType
									}
								}
							} else {
								ferr = fmt.Errorf("bucket object was nil")
							}

							if ferr != nil {
								// error
								ch <- result{
									exit: 1,
									err: fmt.Errorf(
										"failed to get generated videos: %w",
										ferr,
									),
								}
								return
							}
						} else {
							// error
							ch <- result{
								exit: 1,
								err: fmt.Errorf(
									"failed to generate videos: no returned bytes",
								),
							}
							return
						}

						fpath := genFilepath(
							mimeType,
							"video",
							saveVideosToDir,
						)

						writer.verbose(
							verboseMedium,
							vbs,
							"saving video file (%s;%d bytes) to: %s...", mimeType, len(data), fpath,
						)

						if err := os.WriteFile(fpath, data, 0o640); err != nil {
							// error
							ch <- result{
								exit: 1,
								err:  fmt.Errorf("saving video file failed: %s", err),
							}
							return
						} else {
							writer.printWithColorForLevel(
								verboseMinimum,
								"Saved video to file: %s",
								fpath,
							)
						}
					}

					// success
					ch <- result{
						exit: 0,
						err:  nil,
					}
				} else {
					// error
					ch <- result{
						exit: 1,
						err: fmt.Errorf(
							"failed to generate videos: %w",
							err,
						),
					}
					return
				}
			} else {
				printedModelVersion := false

				// iterate generated stream
				for it, err := range gtc.GenerateStreamIterated(
					ctxGenerate,
					contentsForGeneration,
					&opts,
				) {
					if err == nil {
						// print model version
						if !printedModelVersion && len(it.ModelVersion) > 0 {
							printedModelVersion = true

							writer.verbose(verboseMinimum, vbs, "model version: %s", it.ModelVersion)
						}

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
							if it.UsageMetadata.TrafficType != "" && it.UsageMetadata.TrafficType != genai.TrafficTypeUnspecified {
								tokenUsages = append(tokenUsages, fmt.Sprintf(
									"traffic type: %s",
									it.UsageMetadata.TrafficType,
								))
							}
						}

						// append prompts to past generations
						for _, content := range contentsForGeneration {
							pastGenerations = append(pastGenerations, *content)
						}

						// string buffer for model responses
						bufModelResponse := new(strings.Builder)

						retrievedContextTitles := map[string]struct{}{}

						for _, cand := range it.Candidates {
							// url context metadata
							if cand.URLContextMetadata != nil {
								for _, metadata := range cand.URLContextMetadata.URLMetadata {
									writer.verbose(
										verboseMedium,
										vbs,
										"[%s] %s",
										metadata.URLRetrievalStatus,
										metadata.RetrievedURL,
									)
								}
							}

							// content
							if cand.Content != nil {
								for _, part := range cand.Content.Parts {
									// marking begin/end of thoughts
									if withThinking {
										if part.Thought {
											if !thoughtBegan {
												if showThinking {
													writer.printColored(
														color.FgHiYellow,
														"<thought>\n",
													)
												}

												thoughtBegan, thoughtEnded = true, false
												isThinking = true
											}
										} else {
											if thoughtBegan {
												thoughtBegan = false

												if !thoughtEnded {
													if showThinking {
														writer.printColored(
															color.FgHiYellow,
															"</thought>\n",
														)
													}

													thoughtEnded = true
													isThinking = false
												}
											}
										}
									}

									if part.Text != "" {
										if isThinking {
											if showThinking {
												writer.printColored(
													color.FgHiYellow,
													"%s",
													part.Text,
												)
											}
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

										writer.makeSureToEndWithNewline()

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
													"saving image file (%s;%d bytes) to: %s...", part.InlineData.MIMEType, len(part.InlineData.Data), fpath,
												)

												if err := os.WriteFile(fpath, part.InlineData.Data, 0o640); err != nil {
													// error
													ch <- result{
														exit: 1,
														err:  fmt.Errorf("saving image file failed: %s", err),
													}
													return
												} else {
													writer.printWithColorForLevel(
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
											speechCodec, bitRate := speechCodecAndBitRateFromMimeType(part.InlineData.MIMEType)
											if speechCodec == "pcm" && bitRate > 0 { // FIXME: only 'pcm' is supported for now
												// convert,
												if converted, err := pcmToWav(
													part.InlineData.Data,
													bitRate,
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
														"saving speech file (%s;%d bytes) to: %s...",
														mimeType,
														len(converted),
														fpath,
													)

													if err := os.WriteFile(
														fpath,
														converted,
														0o640,
													); err != nil {
														// error
														ch <- result{
															exit: 1,
															err:  fmt.Errorf("saving speech file failed: %s", err),
														}
														return
													} else {
														writer.printWithColorForLevel(
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
										if part.ThoughtSignature != nil {
											thoughtSignature = part.ThoughtSignature
										}

										// flush model response
										pastGenerations = appendAndFlushModelResponse(pastGenerations, bufModelResponse)

										// append function call to past generations
										pastGenerations = append(pastGenerations, genai.Content{
											Role: string(gt.RoleModel),
											Parts: []*genai.Part{
												{
													FunctionCall: &genai.FunctionCall{
														Name: part.FunctionCall.Name,
														Args: part.FunctionCall.Args,
													},
													ThoughtSignature: thoughtSignature,
												},
											},
										})

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
												writer,
												callbackPath,
												toolCallbacksConfirm,
												forceCallDestructiveTools,
												part.FunctionCall,
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

													// append function response to past generations
													pastGenerations = append(pastGenerations, genai.Content{
														Role: string(gt.RoleUser),
														Parts: []*genai.Part{
															{
																FunctionResponse: &genai.FunctionResponse{
																	Name: part.FunctionCall.Name,
																	Response: map[string]any{
																		"output": res,
																	},
																},
																ThoughtSignature: thoughtSignature,
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

												// append function response (not called) to past generations
												pastGenerations = append(pastGenerations, genai.Content{
													Role: string(gt.RoleUser),
													Parts: []*genai.Part{
														{
															FunctionResponse: &genai.FunctionResponse{
																Name: part.FunctionCall.Name,
																Response: map[string]any{
																	"error": fmt.Sprintf(
																		`User chose not to call function '%s'.`,
																		fn,
																	),
																},
															},
															ThoughtSignature: thoughtSignature,
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
															`May I call tool '%s' from '%s'?`,
															// tool name + arguments
															fmt.Sprintf(
																"%s(%s)",
																colorizef(
																	color.FgHiYellow,
																	"%s",
																	part.FunctionCall.Name,
																),
																colorizef(
																	color.FgYellow,
																	"%s",
																	prettify(part.FunctionCall.Args, true),
																),
															),
															// server info
															colorizef(
																color.FgHiBlue,
																"%s",
																stripServerInfo(serverType, serverKey),
															),
														))
													} else {
														okToRun = true
													}
												} else {
													// no matching tool with given server & function name
													writer.warn(
														"No matching tool '%s' from '%s'; given function call was: %s",
														part.FunctionCall.Name,
														stripServerInfo(serverType, serverKey),
														prettify(part.FunctionCall),
													)

													// append function response (no matching tool) to past generations
													pastGenerations = append(pastGenerations, genai.Content{
														Role: string(gt.RoleUser),
														Parts: []*genai.Part{
															{
																FunctionResponse: &genai.FunctionResponse{
																	Name: part.FunctionCall.Name,
																	Response: map[string]any{
																		"error": fmt.Sprintf(
																			"No matching tool '%s' from '%s'; given function call was: %s",
																			part.FunctionCall.Name,
																			stripServerInfo(serverType, serverKey),
																			prettify(part.FunctionCall),
																		),
																	},
																},
																ThoughtSignature: thoughtSignature,
															},
														},
													})
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
														var generated []gt.Prompt
														if res.StructuredContent != nil {
															if raw, err := json.Marshal(res.StructuredContent); err == nil {
																// generated = []gt.Prompt{gt.PromptFromBytes(raw)} // FIXME: http 500 errors occur
																generated = []gt.Prompt{gt.PromptFromText(string(raw))}
															} else {
																// error
																ch <- result{
																	exit: 1,
																	err: fmt.Errorf(
																		"failed to read tool call result: could not marshal structured content (%T): %w",
																		res.StructuredContent,
																		err,
																	),
																}
																return
															}
														} else {
															if prompts, err := gt.MCPCallToolResultToGeminiPrompts(res); err == nil {
																generated = append(generated, prompts...)
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
														}

														// warn that there are tools ignored
														if len(mcpConnsAndTools) > 0 && !recurseOnCallbackResults {
															writer.warn(
																"Not recursing, ignoring the result of '%s'.",
																fn,
															)
														}

														// print the result of execution,
														for _, prompt := range generated {
															if forcePrintCallbackResults ||
																verboseLevel(vbs) >= verboseMinimum {
																writer.printColored(
																	color.FgHiCyan,
																	"%s\n",
																	prompt.String(),
																)
															}

															// and save files if needed
															switch p := prompt.(type) {
															case gt.FilePrompt, gt.BytesPrompt:
																bytes := p.ToPart().InlineData.Data
																mimeType := p.ToPart().InlineData.MIMEType

																if strings.HasPrefix(mimeType, "image/") {
																	if saveImagesToFiles || saveImagesToDir != nil {
																		fpath := genFilepath(
																			mimeType,
																			"image",
																			saveImagesToDir,
																		)

																		writer.verbose(
																			verboseMedium,
																			vbs,
																			"saving image file (%s;%d bytes) to: %s...", mimeType, len(bytes), fpath,
																		)

																		if err := os.WriteFile(fpath, bytes, 0o640); err != nil {
																			// error
																			ch <- result{
																				exit: 1,
																				err:  fmt.Errorf("saving image file failed: %s", err),
																			}
																			return
																		} else {
																			writer.printWithColorForLevel(
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
																			mimeType,
																			len(bytes),
																		)

																		// display on terminal
																		if err := displayImageOnTerminal(
																			bytes,
																			mimeType,
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
																} else if strings.HasPrefix(mimeType, "audio/") {
																	if saveSpeechToDir != nil {
																		// check codec and birtate
																		speechCodec, bitRate := speechCodecAndBitRateFromMimeType(mimeType)
																		if speechCodec == "pcm" && bitRate > 0 { // FIXME: only 'pcm' is supported for now
																			// convert,
																			var ce error
																			if bytes, ce = pcmToWav(
																				bytes,
																				bitRate,
																			); ce == nil {
																				mimeType = mimetype.Detect(bytes).String()
																			}
																		}
																		fpath := genFilepath(
																			mimeType,
																			"audio",
																			saveSpeechToDir,
																		)

																		writer.verbose(
																			verboseMedium,
																			vbs,
																			"saving speech file (%s;%d bytes) to: %s...", mimeType, len(bytes), fpath,
																		)

																		if err := os.WriteFile(
																			fpath,
																			bytes,
																			0o640,
																		); err != nil {
																			// error
																			ch <- result{
																				exit: 1,
																				err:  fmt.Errorf("saving speech file failed: %s", err),
																			}
																			return
																		} else {
																			writer.printWithColorForLevel(
																				verboseMinimum,
																				"Saved speech to file: %s",
																				fpath,
																			)
																		}
																	}
																}
															}
														}

														// flush model response
														pastGenerations = appendAndFlushModelResponse(pastGenerations, bufModelResponse)

														// append function response to past generations
														output := []genai.Part{}
														for _, gen := range generated {
															output = append(output, gen.ToPart())
														}
														parts := []*genai.Part{
															{
																FunctionResponse: &genai.FunctionResponse{
																	Name: part.FunctionCall.Name,
																	Response: map[string]any{
																		"output": output,
																	},
																},
																ThoughtSignature: thoughtSignature,
															},
														}
														pastGenerations = append(pastGenerations, genai.Content{
															Role:  string(gt.RoleUser),
															Parts: parts,
														})
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

													// append function response (not called) to past generations
													pastGenerations = append(pastGenerations, genai.Content{
														Role: string(gt.RoleUser),
														Parts: []*genai.Part{
															{
																FunctionResponse: &genai.FunctionResponse{
																	Name: part.FunctionCall.Name,
																	Response: map[string]any{
																		"error": fmt.Sprintf(
																			`User chose not to call function '%s'.`,
																			fn,
																		),
																	},
																},
																ThoughtSignature: thoughtSignature,
															},
														},
													})
												}
											} else {
												// no matching tool, just print the function call data
												writer.printWithColorForLevel(
													verboseMinimum,
													"No matching tool; given function call was: %s",
													prettify(part.FunctionCall),
												)

												// append function response (no matching tool) to past generations
												pastGenerations = append(pastGenerations, genai.Content{
													Role: string(gt.RoleUser),
													Parts: []*genai.Part{
														{
															FunctionResponse: &genai.FunctionResponse{
																Name: part.FunctionCall.Name,
																Response: map[string]any{
																	"error": fmt.Sprintf(
																		"No matching tool; given function call was: %s",
																		prettify(part.FunctionCall),
																	),
																},
															},
															ThoughtSignature: thoughtSignature,
														},
													},
												})
											}
										} else {
											// just print the function call data
											writer.printWithColorForLevel(
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

							// grounding metadata
							if cand.GroundingMetadata != nil {
								// NOTE: make sure to insert a new line before displaying grounding metadata
								if verboseLevel(vbs) >= verboseMinimum {
									writer.makeSureToEndWithNewline()
								}

								writer.verbose(
									verboseMinimum,
									vbs,
									"ground metadata:\n%s",
									prettify(cand.GroundingMetadata),
								)

								// saved retrieved context titles
								for _, retrieved := range cand.GroundingMetadata.GroundingChunks {
									if retrieved.RetrievedContext != nil {
										if retrieved.RetrievedContext.Title != "" {
											retrievedContextTitles[retrieved.RetrievedContext.Title] = struct{}{}
										} else if retrieved.RetrievedContext.URI != "" {
											retrievedContextTitles[retrieved.RetrievedContext.URI] = struct{}{}
										}
									}
								}
							}

							// citation metadata
							if cand.CitationMetadata != nil {
								// NOTE: make sure to insert a new line before displaying grounding metadata
								if verboseLevel(vbs) >= verboseMinimum {
									writer.makeSureToEndWithNewline()
								}

								writer.verbose(
									verboseMinimum,
									vbs,
									">>> citation metadata:\n%s",
									prettify(cand.CitationMetadata),
								)

								// TODO: do the same thing as grounding metadata above
							}

							// finish reason
							if cand.FinishReason != "" {
								// flush model response
								pastGenerations = appendAndFlushModelResponse(pastGenerations, bufModelResponse)

								writer.makeSureToEndWithNewline() // NOTE: make sure to insert a new line before displaying finish reason

								// print retrieved context titles
								if len(retrievedContextTitles) > 0 {
									titles := []string{}
									for title := range retrievedContextTitles {
										titles = append(titles, title)
									}

									writer.printColored(
										color.FgHiCyan,
										"> Retrieved contexts from file search store: %s\n",
										prettify(titles),
									)
								}

								// TODO: do the same thing as grounding metadata above

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

								if cand.FinishReason == genai.FinishReasonStop {
									// success
									ch <- result{
										exit: 0,
										err:  nil,
									}
								} else {
									// error
									ch <- result{
										exit: 1,
										err:  fmt.Errorf("finished with non-stop reason: %s", cand.FinishReason),
									}
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
			}
		} else {
			// error
			ch <- result{
				exit: 1,
				err:  fmt.Errorf("failed to convert prompts to contents for generation: %w", err),
			}
			return
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
				gtc,
				pastGenerations,
				nil, nil, // NOTE: all prompts and histories for recursion are already appended in `pastGenerations`
				tools, toolConfig, mcpConnsAndTools,
				thoughtSignature,
				p,
			)
		}

		return res.exit, res.err
	}
}

// append and flush model response
func appendAndFlushModelResponse(
	generatedConversations []genai.Content,
	buffer *strings.Builder,
) []genai.Content {
	if buffer.Len() > 0 {
		// if the last conversation is from model, append to it
		if len(generatedConversations) > 0 &&
			generatedConversations[len(generatedConversations)-1].Role == string(gt.RoleModel) {
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
		} else { // or, just append a new model response
			generatedConversations = append(generatedConversations, genai.Content{
				Role: string(gt.RoleModel),
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
	writer outputWriter,
	callbackPath string,
	confirmToolCallbacks map[string]bool,
	forceCallDestructiveTools bool,
	fnCall *genai.FunctionCall,
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
				`May I execute callback '%s' for function '%s'?`,
				// callback path
				colorizef(
					color.FgHiBlue,
					"%s",
					callbackPath,
				),
				// tool name + arguments
				fmt.Sprintf(
					"%s(%s)",
					colorizef(
						color.FgHiYellow,
						"%s",
						fnCall.Name,
					),
					colorizef(
						color.FgYellow,
						"%s",
						prettify(fnCall.Args, true),
					),
				),
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

	return fnCallback, okToRun
}
