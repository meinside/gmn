// serve.go
//
// Things for serving a local STDIO MCP server.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"cloud.google.com/go/storage"
	"github.com/gabriel-vasile/mimetype"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/genai"

	gt "github.com/meinside/gemini-things-go"
	"github.com/meinside/version-go"
)

const (
	mcpFunctionTimeoutSeconds = 3 * 60

	commandTimeoutSeconds = 30
)

// serve MCP server with params
func serve(
	writer outputWriter,
	p params,
) (exit int, err error) {
	writer.verbose(
		verboseMinimum,
		p.Verbose,
		"starting MCP server...",
	)

	// read and apply configs
	var conf config
	if conf, p, err = readAndFillConfig(p, writer); err != nil {
		return 1, fmt.Errorf("failed to read and fill configs: %w", err)
	}

	// files are not supported
	if len(p.Generation.Filepaths) > 0 {
		return 1, fmt.Errorf("files are not supported")
	}

	// run stdio MCP server
	if err = runStdioServer(
		context.TODO(),
		writer,
		conf,
		p,
	); err != nil {
		return 1, err
	}
	return 0, nil
}

// build a MCP server with itself
func buildSelfServer(
	writer outputWriter,
	conf config,
	p params,
) (*mcp.Server, []*mcp.Tool) {
	// new server
	server := mcp.NewServer(
		&mcp.Implementation{
			Name:    mcpServerName,
			Version: version.Build(version.OS | version.Architecture),
		},
		&mcp.ServerOptions{},
	)

	type toolAndHandler struct {
		tool    mcp.Tool
		handler mcp.ToolHandler
	}
	toolsAndHandlers := make([]toolAndHandler, 0)

	// add tools
	//
	// list models (read only, idempotent)
	toolsAndHandlers = append(toolsAndHandlers, toolAndHandler{
		tool: mcp.Tool{
			Name: `gmn_list_models`,
			Description: `This function lists all available Google AI models.
`,
			InputSchema: &jsonschema.Schema{
				Type:     "object",
				ReadOnly: true,
			},
			Annotations: &mcp.ToolAnnotations{
				IdempotentHint: true,
				ReadOnlyHint:   true,
			},
		},
		handler: func(
			ctx context.Context,
			request *mcp.CallToolRequest,
		) (result *mcp.CallToolResult, err error) {
			var gtc *gt.Client
			if gtc, err = gtClient(conf); err == nil {
				var models []*genai.Model
				if models, err = gtc.ListModels(ctx); err == nil {
					var marshalled []byte
					if marshalled, err = json.Marshal(struct {
						Models []*genai.Model `json:"models"`
					}{
						Models: models,
					}); err == nil {
						return &mcp.CallToolResult{
							Content: []mcp.Content{
								&mcp.TextContent{
									Text: string(marshalled),
								},
							},
							StructuredContent: json.RawMessage(marshalled), // structured (JSON)
						}, nil
					} else {
						return &mcp.CallToolResult{
							Content: []mcp.Content{
								&mcp.TextContent{
									Text: fmt.Sprintf("Failed to marshal fetched Google AI models: %s", err),
								},
							},
							IsError: true,
						}, nil
					}
				} else {
					return &mcp.CallToolResult{
						Content: []mcp.Content{
							&mcp.TextContent{
								Text: fmt.Sprintf("Failed to fetch Google AI models: %s", err),
							},
						},
						IsError: true,
					}, nil
				}
			} else {
				return &mcp.CallToolResult{
					Content: []mcp.Content{
						&mcp.TextContent{
							Text: fmt.Sprintf("Failed to initialize Google AI client: %s", err),
						},
					},
					IsError: true,
				}, nil
			}
		},
	})
	//
	// generate text
	toolsAndHandlers = append(toolsAndHandlers, toolAndHandler{
		tool: mcp.Tool{
			Name: `gmn_generate`,
			Description: `This function generates texts/images/speeches/videos by processing the given 'prompt' and optional parameters.

* NOTE:
- If there was any newly-created file, make sure to report to the user about the file's absolute filepath so the user could use it later.
`,
			InputSchema: &jsonschema.Schema{
				Type:     "object",
				ReadOnly: true,
				Properties: map[string]*jsonschema.Schema{
					"prompt": {
						Title:       "prompt",
						Description: `The user's prompt for generation.`,
						Type:        "string",
					},
					"filepaths": {
						Title:       "filepaths",
						Description: `Paths to local files to be processed along with the given 'prompt'. If a path is not absolute, it will be resolved against the current working directory of this MCP server. It will be ignored if 'modality' is 'video'.`,
						Type:        "array",
					},
					"modality": {
						Title:       "modality",
						Description: `The modality of the generation. Must be one of 'text', 'image', 'audio', or 'video'.`,
						Type:        "string",
						Enum: []any{
							"text",
							"image",
							"audio",
							"video",
						},
					},
					"model": {
						Title:       "model",
						Description: `The model to use for generation. If not specified, the default model will be used.`,
						Type:        "string",
					},
					"with_thinking": {
						Title:       "with_thinking",
						Description: `Whether to generate with thinking. If not specified, default value is false. It will be ignored unless 'modality' is 'text'.`,
						Type:        "boolean",
					},
					"with_grounding": {
						Title:       "with_grounding",
						Description: `Whether to generate with grounding, with the helo of Google Search. If not specified, default value is false. It will be ignored unless 'modality' is 'text'.`,
						Type:        "boolean",
					},
					"convert_url": {
						Title:       "convert_url",
						Description: `Whether to convert URLs in the prompt into the corresponding contents. If not specified, default value is false. It will be ignored unless 'modality' is 'text'.`,
						Type:        "boolean",
					},
					"video_firstframe_filepath": {
						Title:       "video_firstframe_filepath",
						Description: `The filepath to the first frame (image) of the video to be generated. It will be ignored unless 'modality' is 'video'.`,
						Type:        "string",
					},
					"video_lastframe_filepath": {
						Title:       "video_lastframe_filepath",
						Description: `The filepath to the last frame (image) of the video to be generated. It will be ignored unless 'modality' is 'video'.`,
						Type:        "string",
					},
					"video_for_extension_filepath": {
						Title:       "video_for_extension_filepath",
						Description: `The filepath to a video which will be extended by the generation. It will be ignored unless 'modality' is 'video'.`,
						Type:        "string",
					},
				},
				Required: []string{
					"prompt",
					"modality",
				},
			},
		},
		handler: func(
			ctx context.Context,
			request *mcp.CallToolRequest,
		) (result *mcp.CallToolResult, err error) {
			p := p // copy launch params

			// convert arguments
			var args map[string]any
			if json.Unmarshal(request.Params.Arguments, &args) != nil {
				return &mcp.CallToolResult{
					Content: []mcp.Content{
						&mcp.TextContent{
							Text: fmt.Sprintf(
								"Failed to convert arguments to `%T`: %s",
								args,
								err,
							),
						},
					},
					IsError: true,
				}, nil
			}

			// get 'prompt',
			var prompt *string
			prompt, err = gt.FuncArg[string](args, "prompt")
			if err == nil {
				// get 'filepaths'
				var filepaths []*string = nil
				fps, _ := gt.FuncArg[[]any](args, "filepaths")
				if fps != nil {
					for _, fp := range *fps {
						if pth, ok := fp.(string); ok {
							filepaths = append(filepaths, ptr(expandPath(pth)))
						}
					}
				}

				// get 'modality',
				var modality *string
				modality, err = gt.FuncArg[string](args, "modality")
				if err == nil {
					var responseModalities []string = nil

					// get 'model',
					model, _ := gt.FuncArg[string](args, "model")
					switch *modality {
					case "text":
						if model == nil {
							if p.Configuration.GoogleAIModel != nil {
								model = p.Configuration.GoogleAIModel
							} else if conf.GoogleAIModel != nil {
								model = conf.GoogleAIModel
							} else {
								model = ptr(defaultGoogleAIModel)
							}
						}
					case "image":
						if model == nil {
							if conf.GoogleAIImageGenerationModel != nil {
								model = conf.GoogleAIImageGenerationModel
							} else {
								model = ptr(string(defaultGoogleAIImageGenerationModel))
							}
						}
					case "audio":
						if model == nil {
							if conf.GoogleAISpeechGenerationModel != nil {
								model = conf.GoogleAISpeechGenerationModel
							} else {
								model = ptr(string(defaultGoogleAISpeechGenerationModel))
							}
						}
					case "video":
						if model == nil {
							if conf.GoogleAIVideoGenerationModel != nil {
								model = conf.GoogleAIVideoGenerationModel
							} else {
								model = ptr(string(defaultGoogleAIVideoGenerationModel))
							}
						}
					}

					// get system instruction,
					p.Generation.DetailedOptions.SystemInstruction = nil
					switch *modality {
					case "text":
						if p.Generation.DetailedOptions.SystemInstruction == nil {
							if conf.SystemInstruction != nil {
								p.Generation.DetailedOptions.SystemInstruction = conf.SystemInstruction
							} else {
								p.Generation.DetailedOptions.SystemInstruction = ptr(defaultSystemInstruction())
							}
						}
					}

					// get appropriate response modalities,
					switch *modality {
					case "image":
						responseModalities = []string{
							string(genai.ModalityText),
							string(genai.ModalityImage),
						}
					case "audio":
						responseModalities = []string{
							string(genai.ModalityAudio),
						}
					}

					// get 'with_thinking',
					thinkingOn := ptr(false)
					switch *modality {
					case "text":
						withThinking, _ := gt.FuncArg[bool](args, "with_thinking")
						if withThinking != nil {
							thinkingOn = withThinking
						}
					}

					// get 'with_grounding',
					withGrounding := ptr(false)
					switch *modality {
					case "text":
						grounding, _ := gt.FuncArg[bool](args, "with_grounding")
						if grounding != nil {
							withGrounding = grounding
						}
					}

					// get 'convert_url',
					convertURL := ptr(false)
					switch *modality {
					case "text":
						withURLConversion, _ := gt.FuncArg[bool](args, "convert_url")
						if withURLConversion != nil {
							convertURL = withURLConversion
						}
					}

					// create a client,
					var gtc *gt.Client
					gtc, err = gtClient(
						conf,
						gt.WithModel(*model),
					)
					if err == nil {
						gtc.SetSystemInstructionFunc(nil)

						writer.verbose(
							verboseMedium,
							p.Verbose,
							"generating response with modality: %s (%s), model: '%s', with grounding: %v, with thinking: %v, convert url: %v, and prompt: '%s'",
							*modality,
							prettify(responseModalities, true),
							*model,
							*withGrounding,
							*thinkingOn,
							*convertURL,
							*prompt,
						)

						// setup tools
						var tools []*genai.Tool = nil
						if *withGrounding {
							tools = []*genai.Tool{
								{
									GoogleSearch: &genai.GoogleSearch{},
								},
							}
						}

						// convert prompt
						prompts := []gt.Prompt{}
						promptFiles := map[string][]byte{}
						if *convertURL { // (convert urls to file prompts, and read local files)
							p.Generation.Prompt = prompt
							replacedPrompt, extractedPromptsWithURL := replaceURLsInPrompt(writer, conf, p)

							// add prompt with urls replaced with some placeholders
							prompts = append(prompts, gt.PromptFromText(replacedPrompt))
							for customURL, data := range extractedPromptsWithURL {
								if customURL.isLink() {
									promptFiles[customURL.url()] = data
								} else if customURL.isYoutube() {
									prompts = append(prompts, gt.PromptFromURI(customURL.url(), "video/mp4"))
								}
							}
						} else { // (just use the original prompt)
							prompts = append(prompts, gt.PromptFromText(*prompt))
						}

						// read bytes from url prompts and local files, and append them as prompts
						var files []openedFile
						if files, err = openFilesForPrompt(
							promptFiles,
							filepaths,
						); err == nil {
							for _, file := range files {
								prompts = append(prompts, gt.PromptFromFile(file.filename, file.reader))
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
						} else {
							return &mcp.CallToolResult{
								Content: []mcp.Content{
									&mcp.TextContent{
										Text: fmt.Sprintf(
											"Failed to open files: %s",
											err,
										),
									},
								},
								IsError: true,
							}, nil
						}

						ctxContents, cancelContents := context.WithTimeout(ctx, mcpFunctionTimeoutSeconds*time.Second)
						defer cancelContents()

						// generate,
						var contentsForGeneration []*genai.Content
						if contentsForGeneration, err = gtc.PromptsToContents(
							ctxContents,
							prompts,
							nil,
						); err == nil {
							ctxGenerate, cancelGenerate := context.WithTimeout(ctx, mcpFunctionTimeoutSeconds*time.Second)
							defer cancelGenerate()

							if *modality != "video" { // generate text, image, speech, ...
								var res *genai.GenerateContentResponse
								if res, err = gtc.Generate(
									ctxGenerate,
									contentsForGeneration,
									&genai.GenerateContentConfig{
										Tools: tools,
										ThinkingConfig: &genai.ThinkingConfig{
											IncludeThoughts: *thinkingOn,
										},
										ResponseModalities: responseModalities,
									},
								); err == nil {
									content := []mcp.Content{}
									for _, candidate := range res.Candidates {
										if candidate.Content.Role != string(gt.RoleModel) {
											continue
										}
										for i, part := range candidate.Content.Parts {
											if len(part.Text) > 0 {
												writer.verbose(
													verboseMaximum,
													p.Verbose,
													"text[%d]: '%s'", i, part.Text,
												)

												content = append(content, &mcp.TextContent{
													Text: part.Text,
												})
											} else if part.InlineData != nil {
												bytes := part.InlineData.Data
												mimeType := part.InlineData.MIMEType

												writer.verbose(
													verboseMaximum,
													p.Verbose,
													"image data[%d]: %d bytes (%s)", i, len(bytes), mimeType,
												)

												if strings.HasPrefix(part.InlineData.MIMEType, "image/") {
													content = append(
														content,
														&mcp.TextContent{
															Text: fmt.Sprintf(
																"Generated an image file with %d bytes(%s).",
																len(bytes),
																mimeType,
															),
														},
														/*
															&mcp.ImageContent{
																Data:     bytes,
																MIMEType: mimeType,
															},
														*/
													)

													// save to a file
													fpath := genFilepath(
														mimeType,
														"image",
														p.Generation.Image.SaveToDir,
													)
													if err = os.WriteFile(fpath, bytes, 0o640); err == nil {
														content = append(content,
															&mcp.TextContent{
																Text: fmt.Sprintf(
																	"Saved an image to the following filepath: '%s'",
																	fpath,
																),
															},
														)
													} else {
														writer.errorWithColorForLevel(
															verboseMaximum,
															"failed to save image file: %s",
															err,
														)
													}
												} else if strings.HasPrefix(part.InlineData.MIMEType, "audio/") {
													// if it is in PCM, convert it to WAV
													speechCodec, bitRate := speechCodecAndBitRateFromMimeType(mimeType)
													if speechCodec == "pcm" && bitRate > 0 { // FIXME: only 'pcm' is supported for now
														// convert,
														if converted, err := pcmToWav(
															part.InlineData.Data,
															bitRate,
														); err == nil {
															bytes = converted
															mimeType = mimetype.Detect(converted).String()
														}
													}

													content = append(
														content,
														&mcp.TextContent{
															Text: fmt.Sprintf(
																"Generated an audio file with %d bytes(%s).",
																len(bytes),
																mimeType,
															),
														},
														/*
															&mcp.AudioContent{
																Data:     bytes,
																MIMEType: mimeType,
															},
														*/
													)

													// save to a file
													fpath := genFilepath(
														mimeType,
														"audio",
														p.Generation.Speech.SaveToDir,
													)
													if err = os.WriteFile(fpath, bytes, 0o640); err == nil {
														content = append(content,
															&mcp.TextContent{
																Text: fmt.Sprintf(
																	"Saved an audio to the following filepath: '%s'",
																	fpath,
																),
															},
														)
													} else {
														writer.errorWithColorForLevel(
															verboseMaximum,
															"failed to save audio file: %s",
															err,
														)
													}
												} else {
													writer.errorWithColorForLevel(
														verboseMaximum,
														"unknown inline data type: %s", part.InlineData.MIMEType,
													)
												}
											}
										}
									}
									return &mcp.CallToolResult{
										Content: content,
									}, nil
								}
							} else { // generate video,
								// convert arguments to images and videos
								var firstFrame, lastFrame *genai.Image
								var videoForExtension *genai.Video
								var firstFrameFilepath *string
								if firstFrameFilepath, err = gt.FuncArg[string](args, "video_firstframe_filepath"); err == nil && firstFrameFilepath != nil {
									if bs, ferr := os.ReadFile(*firstFrameFilepath); ferr == nil {
										firstFrame = &genai.Image{
											ImageBytes: bs,
											MIMEType:   mimetype.Detect(bs).String(),
										}
									} else {
										// error
										writer.errorWithColorForLevel(
											verboseMaximum,
											"failed to read first frame file for video generation: %s",
											ferr,
										)

										return &mcp.CallToolResult{
											Content: []mcp.Content{
												&mcp.TextContent{
													Text: fmt.Sprintf(
														"failed to read first frame file for video generation: %s",
														ferr,
													),
												},
											},
											IsError: true,
										}, nil
									}
								}
								var lastFrameFilepath *string
								if lastFrameFilepath, err = gt.FuncArg[string](args, "video_lastframe_filepath"); err == nil && lastFrameFilepath != nil {
									if bs, ferr := os.ReadFile(*lastFrameFilepath); ferr == nil {
										lastFrame = &genai.Image{
											ImageBytes: bs,
											MIMEType:   mimetype.Detect(bs).String(),
										}
									} else {
										// error
										writer.errorWithColorForLevel(
											verboseMaximum,
											"failed to read last frame file for video generation: %s",
											ferr,
										)

										return &mcp.CallToolResult{
											Content: []mcp.Content{
												&mcp.TextContent{
													Text: fmt.Sprintf(
														"failed to read last frame file for video generation: %s",
														ferr,
													),
												},
											},
											IsError: true,
										}, nil
									}
								}
								var videoForExtensionFilepath *string
								if videoForExtensionFilepath, err = gt.FuncArg[string](args, "video_for_extension_filepath"); err == nil && videoForExtensionFilepath != nil {
									if bs, ferr := os.ReadFile(*videoForExtensionFilepath); ferr == nil {
										videoForExtension = &genai.Video{
											VideoBytes: bs,
											MIMEType:   mimetype.Detect(bs).String(),
										}
									} else {
										// error
										writer.errorWithColorForLevel(
											verboseMaximum,
											"failed to read video file for video generation: %s",
											ferr,
										)

										return &mcp.CallToolResult{
											Content: []mcp.Content{
												&mcp.TextContent{
													Text: fmt.Sprintf(
														"failed to read video file for video generation: %s",
														ferr,
													),
												},
											},
											IsError: true,
										}, nil
									}
								}

								options := &genai.GenerateVideosConfig{
									EnhancePrompt:    true,
									PersonGeneration: "allow_adult",
								}
								if lastFrame != nil {
									options.LastFrame = lastFrame
								}

								// TODO: reference images

								content := []mcp.Content{}
								var res *genai.GenerateVideosResponse
								if res, err = gtc.GenerateVideos(ctxGenerate, prompt, firstFrame, videoForExtension, options); err == nil {
									for i, video := range res.GeneratedVideos {
										var bytes []byte
										var mimeType string
										if len(video.Video.VideoBytes) > 0 {
											bytes = video.Video.VideoBytes
											// mimeType = mimetype.Detect(data).String()
											mimeType = video.Video.MIMEType
										} else if len(video.Video.URI) > 0 {
											var ferr error
											if obj := gtc.Storage().Bucket(gtc.GetBucketName()).Object(video.Video.URI); obj == nil {
												var reader *storage.Reader
												if reader, ferr = obj.NewReader(ctxGenerate); ferr == nil {
													defer func() { _ = reader.Close() }()

													if bytes, ferr = io.ReadAll(reader); ferr == nil {
														mimeType = video.Video.MIMEType
													}
												}
											} else {
												ferr = fmt.Errorf("bucket object was nil")
											}

											if ferr != nil {
												// error
												writer.errorWithColorForLevel(
													verboseMaximum,
													"failed to get generated videos: %s",
													ferr,
												)

												return &mcp.CallToolResult{
													Content: []mcp.Content{
														&mcp.TextContent{
															Text: fmt.Sprintf(
																"failed to get generated videos: %s",
																ferr,
															),
														},
													},
													IsError: true,
												}, nil
											}
										} else {
											// error
											writer.errorWithColorForLevel(
												verboseMaximum,
												"failed to generate videos: no returned bytes",
											)

											return &mcp.CallToolResult{
												Content: []mcp.Content{
													&mcp.TextContent{
														Text: "failed to generate videos: no returned bytes",
													},
												},
												IsError: true,
											}, nil
										}

										writer.verbose(
											verboseMaximum,
											p.Verbose,
											"video data[%d]: %d bytes (%s)", i, len(bytes), mimeType,
										)

										content = append(
											content,
											&mcp.TextContent{
												Text: fmt.Sprintf(
													"Generated a video file with %d bytes(%s).",
													len(bytes),
													mimeType,
												),
											},
											/*
												&mcp.VideoContent{
													Data:     bytes,
													MIMEType: mimeType,
												},
											*/
										)

										// save to a file
										fpath := genFilepath(
											mimeType,
											"video",
											p.Generation.Video.SaveToDir,
										)
										if err = os.WriteFile(fpath, bytes, 0o640); err == nil {
											content = append(content,
												&mcp.TextContent{
													Text: fmt.Sprintf(
														"Saved a video to the following filepath: '%s'",
														fpath,
													),
												},
											)
										} else {
											writer.errorWithColorForLevel(
												verboseMaximum,
												"failed to save video file: %s",
												err,
											)
										}
									}

									return &mcp.CallToolResult{
										Content: content,
									}, nil
								}
							}
						} else {
							err = fmt.Errorf("failed to convert prompts for generation: %w", err)
						}
					}
				}
			} else {
				err = fmt.Errorf("failed to get parameter 'prompt': %w", err)
			}

			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{
						Text: fmt.Sprintf(
							"Failed to generate: %s",
							err,
						),
					},
				},
				IsError: true,
			}, nil
		},
	})
	//
	// TODO: generate embeddings with text (readonly)
	//
	// get current working directory (readonly, idempotent)
	toolsAndHandlers = append(toolsAndHandlers, toolAndHandler{
		tool: mcp.Tool{
			Name: `gmn_get_cwd`,
			Description: `This function returns the current working directory (absolute path).

* NOTE:
- It is advised to call this function before performing any task which handles filepaths.
`,
			InputSchema: &jsonschema.Schema{
				Type:     "object",
				ReadOnly: true,
			},
			Annotations: &mcp.ToolAnnotations{
				IdempotentHint: true,
				ReadOnlyHint:   true,
			},
		},
		handler: func(
			ctx context.Context,
			request *mcp.CallToolRequest,
		) (result *mcp.CallToolResult, err error) {
			// get current working directory
			var cwd string
			if cwd, err = os.Getwd(); err == nil {
				result := struct {
					Cwd string `json:"currentWorkingDirectory"`
				}{
					Cwd: cwd,
				}

				var marshalled []byte
				if marshalled, err = json.Marshal(result); err == nil {
					return &mcp.CallToolResult{
						Content: []mcp.Content{
							&mcp.TextContent{
								Text: string(marshalled),
							},
						},
						StructuredContent: json.RawMessage(marshalled), // structured (JSON)
					}, nil
				} else {
					return &mcp.CallToolResult{
						Content: []mcp.Content{
							&mcp.TextContent{
								Text: fmt.Sprintf("Failed to marshal current working directory: %s", err),
							},
						},
						IsError: true,
					}, nil
				}
			} else {
				return &mcp.CallToolResult{
					Content: []mcp.Content{
						&mcp.TextContent{
							Text: fmt.Sprintf("Failed to get current working directory: %s", err),
						},
					},
					IsError: true,
				}, nil
			}
		},
	})
	//
	// stat a file at given path (readonly)
	toolsAndHandlers = append(toolsAndHandlers, toolAndHandler{
		tool: mcp.Tool{
			Name: `gmn_stat_file`,
			Description: `This function returns the state of a file or directory.

* NOTE:
- It is advised to call this function before accessing or handling files and/or directories.
`,
			InputSchema: &jsonschema.Schema{
				Type:     "object",
				ReadOnly: true,
				Properties: map[string]*jsonschema.Schema{
					"filepath": {
						Title:       "filepath",
						Description: `An absolute path to a local file or directory.`,
						Type:        "string",
					},
				},
				Required: []string{
					"filepath",
				},
			},
			Annotations: &mcp.ToolAnnotations{
				ReadOnlyHint: true,
			},
		},
		handler: func(
			ctx context.Context,
			request *mcp.CallToolRequest,
		) (result *mcp.CallToolResult, err error) {
			// convert arguments
			var args map[string]any
			if json.Unmarshal(request.Params.Arguments, &args) != nil {
				return &mcp.CallToolResult{
					Content: []mcp.Content{
						&mcp.TextContent{
							Text: fmt.Sprintf(
								"Failed to convert arguments to `%T`: %s",
								args,
								err,
							),
						},
					},
					IsError: true,
				}, nil
			}

			// get 'filepath',
			var filepath *string
			filepath, err = gt.FuncArg[string](args, "filepath")
			if err == nil {
				// get stat of a file/directory
				var stat os.FileInfo
				if stat, err = os.Stat(*filepath); err == nil {
					result := fileInfoToJSON(stat, *filepath)

					return &mcp.CallToolResult{
						Content: []mcp.Content{
							&mcp.TextContent{
								Text: result,
							},
						},
						StructuredContent: json.RawMessage(result), // structured (JSON)
					}, nil
				}
			} else {
				err = fmt.Errorf("failed to get parameter 'filepath': %w", err)
			}

			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{
						Text: fmt.Sprintf(
							"Failed to stat file: %s",
							err,
						),
					},
				},
				IsError: true,
			}, nil
		},
	})
	//
	// get mime type of a file at given path (readonly)
	toolsAndHandlers = append(toolsAndHandlers, toolAndHandler{
		tool: mcp.Tool{
			Name: `gmn_get_mimetype`,
			Description: `This function returns the mime type of a file at given path.

* NOTE:
- It is advised to call this function before reading a file.
`,
			InputSchema: &jsonschema.Schema{
				Type:     "object",
				ReadOnly: true,
				Properties: map[string]*jsonschema.Schema{
					"filepath": {
						Title:       "filepath",
						Description: `An absolute path to a local file.`,
						Type:        "string",
					},
				},
				Required: []string{
					"filepath",
				},
			},
			Annotations: &mcp.ToolAnnotations{
				ReadOnlyHint: true,
			},
		},
		handler: func(
			ctx context.Context,
			request *mcp.CallToolRequest,
		) (result *mcp.CallToolResult, err error) {
			// convert arguments
			var args map[string]any
			if json.Unmarshal(request.Params.Arguments, &args) != nil {
				return &mcp.CallToolResult{
					Content: []mcp.Content{
						&mcp.TextContent{
							Text: fmt.Sprintf(
								"Failed to convert arguments to `%T`: %s",
								args,
								err,
							),
						},
					},
					IsError: true,
				}, nil
			}

			// get 'filepath',
			var filepath *string
			filepath, err = gt.FuncArg[string](args, "filepath")
			if err == nil {
				// get mime type
				var mime *mimetype.MIME
				if mime, err = mimetype.DetectFile(*filepath); err == nil {
					result := struct {
						Filepath  string `json:"filepath"`
						MimeType  string `json:"mimeType"`
						Extension string `json:"extension"`
					}{
						Filepath:  *filepath,
						MimeType:  mime.String(),
						Extension: mime.Extension(),
					}

					var marshalled []byte
					if marshalled, err = json.Marshal(result); err == nil {
						return &mcp.CallToolResult{
							Content: []mcp.Content{
								&mcp.TextContent{
									Text: string(marshalled),
								},
							},
							StructuredContent: json.RawMessage(marshalled), // structured (JSON)
						}, nil
					} else {
						return &mcp.CallToolResult{
							Content: []mcp.Content{
								&mcp.TextContent{
									Text: fmt.Sprintf("Failed to marshal read file: %s", err),
								},
							},
							IsError: true,
						}, nil
					}
				} else {
					err = fmt.Errorf("failed to get mime type: %w", err)
				}
			} else {
				err = fmt.Errorf("failed to get parameter 'filepath': %w", err)
			}

			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{
						Text: fmt.Sprintf(
							"Failed to stat file: %s",
							err,
						),
					},
				},
				IsError: true,
			}, nil
		},
	})
	//
	// list files at path (readonly)
	toolsAndHandlers = append(toolsAndHandlers, toolAndHandler{
		tool: mcp.Tool{
			Name: `gmn_list_files`,
			Description: `This function lists all files at a given path.
`,
			InputSchema: &jsonschema.Schema{
				Type:     "object",
				ReadOnly: true,
				Properties: map[string]*jsonschema.Schema{
					"dirpath": {
						Title:       "dirpath",
						Description: `An absolute path to a local directory.`,
						Type:        "string",
					},
				},
				Required: []string{
					"dirpath",
				},
			},
			Annotations: &mcp.ToolAnnotations{
				ReadOnlyHint: true,
			},
		},
		handler: func(
			ctx context.Context,
			request *mcp.CallToolRequest,
		) (result *mcp.CallToolResult, err error) {
			// convert arguments
			var args map[string]any
			if json.Unmarshal(request.Params.Arguments, &args) != nil {
				return &mcp.CallToolResult{
					Content: []mcp.Content{
						&mcp.TextContent{
							Text: fmt.Sprintf(
								"Failed to convert arguments to `%T`: %s",
								args,
								err,
							),
						},
					},
					IsError: true,
				}, nil
			}

			// get 'dirpath',
			var dirpath *string
			dirpath, err = gt.FuncArg[string](args, "dirpath")
			if err == nil {
				// list all files at `dirpath` (not recursive)
				var entries []os.DirEntry
				if entries, err = os.ReadDir(*dirpath); err == nil {
					result := dirEntriesToJSON(entries, *dirpath)

					return &mcp.CallToolResult{
						Content: []mcp.Content{
							&mcp.TextContent{
								Text: result,
							},
						},
						StructuredContent: json.RawMessage(result), // structured (JSON)
					}, nil
				}
			} else {
				err = fmt.Errorf("failed to get parameter 'dirpath': %w", err)
			}

			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{
						Text: fmt.Sprintf(
							"Failed to list files: %s",
							err,
						),
					},
				},
				IsError: true,
			}, nil
		},
	})
	//
	// read content from a file at path (readonly, destructive)
	toolsAndHandlers = append(toolsAndHandlers, toolAndHandler{
		tool: mcp.Tool{
			Name: `gmn_read_text_file`,
			Description: `This function reads a plain text file at a given filepath.

* NOTE:
- Make sure to report to the user if this function was called and the specified file was successfully read.
`,
			InputSchema: &jsonschema.Schema{
				Type:     "object",
				ReadOnly: true,
				Properties: map[string]*jsonschema.Schema{
					"filepath": {
						Title:       "filepath",
						Description: `An absolute path of a file that will be read.`,
						Type:        "string",
					},
				},
				Required: []string{
					"filepath",
				},
			},
			Annotations: &mcp.ToolAnnotations{
				DestructiveHint: ptr(true),
				ReadOnlyHint:    true,
			},
		},
		handler: func(
			ctx context.Context,
			request *mcp.CallToolRequest,
		) (result *mcp.CallToolResult, err error) {
			// convert arguments
			var args map[string]any
			if json.Unmarshal(request.Params.Arguments, &args) != nil {
				return &mcp.CallToolResult{
					Content: []mcp.Content{
						&mcp.TextContent{
							Text: fmt.Sprintf(
								"Failed to convert arguments to `%T`: %s",
								args,
								err,
							),
						},
					},
					IsError: true,
				}, nil
			}

			// get 'filepath',
			var filepath *string
			filepath, err = gt.FuncArg[string](args, "filepath")
			if err == nil {
				// read a file at filepath
				var content []byte
				if content, err = os.ReadFile(*filepath); err == nil {
					mimeType := mimetype.Detect(content)
					if mimeType.Is("text/plain") {
						result := struct {
							Filepath string `json:"filepath"`
							Content  string `json:"content"`
						}{
							Filepath: *filepath,
							Content:  string(content),
						}

						var marshalled []byte
						if marshalled, err = json.Marshal(result); err == nil {
							return &mcp.CallToolResult{
								Content: []mcp.Content{
									&mcp.TextContent{
										Text: string(marshalled),
									},
								},
								StructuredContent: json.RawMessage(marshalled), // structured (JSON)
							}, nil
						} else {
							return &mcp.CallToolResult{
								Content: []mcp.Content{
									&mcp.TextContent{
										Text: fmt.Sprintf("Failed to marshal read file: %s", err),
									},
								},
								IsError: true,
							}, nil
						}
					} else {
						err = fmt.Errorf("given file '%s' is not in text/plain format: %s", *filepath, mimeType.String())
					}
				}
			} else {
				err = fmt.Errorf("failed to get parameter 'filepath': %w", err)
			}

			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{
						Text: fmt.Sprintf(
							"Failed to read file: %s",
							err,
						),
					},
				},
				IsError: true,
			}, nil
		},
	})
	//
	// create a file with given content (destructive)
	toolsAndHandlers = append(toolsAndHandlers, toolAndHandler{
		tool: mcp.Tool{
			Name: `gmn_create_text_file`,
			Description: `This function creates a plain text file at a given filepath.

* CAUTION:
- There should not be an existing file at the given path.
- This function should not be used for creating binary files due to the risk of file corruption.

* NOTE:
- Make sure to report to the user if this function was called and the specified file was successfully created.
`,
			InputSchema: &jsonschema.Schema{
				Type:     "object",
				ReadOnly: true,
				Properties: map[string]*jsonschema.Schema{
					"content": {
						Title:       "content",
						Description: "A plain text content of a file that will be newly created.",
						Type:        "string",
					},
					"filepath": {
						Title:       "filepath",
						Description: `An absolute path of a file that will be newly created.`,
						Type:        "string",
					},
				},
				Required: []string{
					"content",
					"filepath",
				},
			},
			Annotations: &mcp.ToolAnnotations{
				DestructiveHint: ptr(true),
			},
		},
		handler: func(
			ctx context.Context,
			request *mcp.CallToolRequest,
		) (result *mcp.CallToolResult, err error) {
			// convert arguments
			var args map[string]any
			if json.Unmarshal(request.Params.Arguments, &args) != nil {
				return &mcp.CallToolResult{
					Content: []mcp.Content{
						&mcp.TextContent{
							Text: fmt.Sprintf(
								"Failed to convert arguments to `%T`: %s",
								args,
								err,
							),
						},
					},
					IsError: true,
				}, nil
			}

			// get 'filepath',
			var filepath *string
			filepath, err = gt.FuncArg[string](args, "filepath")
			if err == nil {
				// get 'content'
				var content *string
				content, err = gt.FuncArg[string](args, "content")
				if err == nil {
					// create a file
					if err = os.WriteFile(
						*filepath,
						[]byte(*content),
						0o644,
					); err == nil {
						return &mcp.CallToolResult{
							Content: []mcp.Content{
								&mcp.TextContent{
									Text: fmt.Sprintf("File was successfully created at path: '%s'", *filepath),
								},
							},
						}, nil
					}
				} else {
					err = fmt.Errorf("failed to get parameter 'content': %w", err)
				}
			} else {
				err = fmt.Errorf("failed to get parameter 'filepath': %w", err)
			}

			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{
						Text: fmt.Sprintf(
							"Failed to create text file: %s",
							err,
						),
					},
				},
				IsError: true,
			}, nil
		},
	})
	//
	// delete a file at path (destructive)
	toolsAndHandlers = append(toolsAndHandlers, toolAndHandler{
		tool: mcp.Tool{
			Name: `gmn_delete_file`,
			Description: `This function deletes a file at a given filepath.

* NOTE:
- Make sure to report to the user if this function was called and the specified file was successfully deleted.
`,
			InputSchema: &jsonschema.Schema{
				Type:     "object",
				ReadOnly: true,
				Properties: map[string]*jsonschema.Schema{
					"filepath": {
						Title:       "filepath",
						Description: `An absolute path of a file that will be deleted.`,
						Type:        "string",
					},
				},
				Required: []string{
					"filepath",
				},
			},
			Annotations: &mcp.ToolAnnotations{
				DestructiveHint: ptr(true),
			},
		},
		handler: func(
			ctx context.Context,
			request *mcp.CallToolRequest,
		) (result *mcp.CallToolResult, err error) {
			// convert arguments
			var args map[string]any
			if json.Unmarshal(request.Params.Arguments, &args) != nil {
				return &mcp.CallToolResult{
					Content: []mcp.Content{
						&mcp.TextContent{
							Text: fmt.Sprintf(
								"Failed to convert arguments to `%T`: %s",
								args,
								err,
							),
						},
					},
					IsError: true,
				}, nil
			}

			// get 'filepath',
			var filepath *string
			filepath, err = gt.FuncArg[string](args, "filepath")
			if err == nil {
				// delete a file
				if err = os.Remove(*filepath); err == nil {
					return &mcp.CallToolResult{
						Content: []mcp.Content{
							&mcp.TextContent{
								Text: fmt.Sprintf("File was successfully deleted: '%s'", *filepath),
							},
						},
					}, nil
				}
			} else {
				err = fmt.Errorf("failed to get parameter 'filepath': %w", err)
			}

			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{
						Text: fmt.Sprintf(
							"Failed to delete file: %s",
							err,
						),
					},
				},
				IsError: true,
			}, nil
		},
	})
	//
	// move a file (destructive)
	toolsAndHandlers = append(toolsAndHandlers, toolAndHandler{
		tool: mcp.Tool{
			Name: `gmn_move_file`,
			Description: `This function moves a file at a given filepath to another filepath.

* NOTE:
- Make sure to report to the user if this function was called and the specified file was successfully moved.
`,
			InputSchema: &jsonschema.Schema{
				Type:     "object",
				ReadOnly: true,
				Properties: map[string]*jsonschema.Schema{
					"from": {
						Title:       "from",
						Description: `An original path (absolute) of a file that will be moved.`,
						Type:        "string",
					},
					"to": {
						Title:       "to",
						Description: `A destination path (absolute) of a moved file.`,
						Type:        "string",
					},
				},
				Required: []string{
					"from",
					"to",
				},
			},
			Annotations: &mcp.ToolAnnotations{
				DestructiveHint: ptr(true),
			},
		},
		handler: func(
			ctx context.Context,
			request *mcp.CallToolRequest,
		) (result *mcp.CallToolResult, err error) {
			// convert arguments
			var args map[string]any
			if json.Unmarshal(request.Params.Arguments, &args) != nil {
				return &mcp.CallToolResult{
					Content: []mcp.Content{
						&mcp.TextContent{
							Text: fmt.Sprintf(
								"Failed to convert arguments to `%T`: %s",
								args,
								err,
							),
						},
					},
					IsError: true,
				}, nil
			}

			// get 'from',
			var fromFilepath *string
			fromFilepath, err = gt.FuncArg[string](args, "from")
			if err == nil {
				var toFilepath *string
				toFilepath, err = gt.FuncArg[string](args, "to")
				if err == nil {
					// move file
					if err = os.Rename(*fromFilepath, *toFilepath); err == nil {
						return &mcp.CallToolResult{
							Content: []mcp.Content{
								&mcp.TextContent{
									Text: fmt.Sprintf("File was successfully moved: '%s' -> '%s'", *fromFilepath, *toFilepath),
								},
							},
						}, nil
					}
				} else {
					err = fmt.Errorf("failed to get parameter 'to': %w", err)
				}
			} else {
				err = fmt.Errorf("failed to get parameter 'from': %w", err)
			}

			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{
						Text: fmt.Sprintf(
							"Failed to move file: %s",
							err,
						),
					},
				},
				IsError: true,
			}, nil
		},
	})
	//
	// run a bash command (destructive)
	toolsAndHandlers = append(toolsAndHandlers, toolAndHandler{
		tool: mcp.Tool{
			Name: `gmn_run_cmdline`,
			Description: fmt.Sprintf(`This function executes a given bash commandline and returns the resulting output.

* RULES:
- The commandline must be in one line, and should be escaped correctly.

* CAUTION:
- Never pass malicious input or non-existing commands to this function, as it will be executed as a shell command.
- This function will fail with timeout if the commandline takes %d seconds or longer to finish.
`, commandTimeoutSeconds),
			InputSchema: &jsonschema.Schema{
				Type:     "object",
				ReadOnly: true,
				Properties: map[string]*jsonschema.Schema{
					"cmdline": {
						Title:       "cmdline",
						Description: `A bash commandline.`,
						Type:        "string",
					},
				},
				Required: []string{
					"cmdline",
				},
			},
			Annotations: &mcp.ToolAnnotations{
				DestructiveHint: ptr(true),
			},
		},
		handler: func(
			ctx context.Context,
			request *mcp.CallToolRequest,
		) (result *mcp.CallToolResult, err error) {
			// convert arguments
			var args map[string]any
			if json.Unmarshal(request.Params.Arguments, &args) != nil {
				return &mcp.CallToolResult{
					Content: []mcp.Content{
						&mcp.TextContent{
							Text: fmt.Sprintf(
								"Failed to convert arguments to `%T`: %s",
								args,
								err,
							),
						},
					},
					IsError: true,
				}, nil
			}

			// get 'cmdline',
			var cmdline *string
			cmdline, err = gt.FuncArg[string](args, "cmdline")
			if err == nil {
				// execute cmdline
				var command string
				var args []string
				if command, args, err = parseCommandline(*cmdline); err == nil {
					// command timeout
					cmdCtx, cancel := context.WithTimeout(context.Background(), commandTimeoutSeconds*time.Second)
					defer cancel()

					var stdout, stderr string
					var exit int
					if stdout, stderr, exit, err = runCommandWithContext(cmdCtx, command, args...); err == nil {
						result := struct {
							Cmdline  string `json:"cmdline"`
							ExitCode int    `json:"exitCode"`
							Output   string `json:"output,omitempty"`
							Error    string `json:"error,omitempty"`
						}{
							Cmdline:  *cmdline,
							ExitCode: exit,
							Output:   stdout,
							Error:    stderr,
						}

						var marshalled []byte
						if marshalled, err = json.Marshal(result); err == nil {
							return &mcp.CallToolResult{
								Content: []mcp.Content{
									&mcp.TextContent{
										Text: string(marshalled),
									},
								},
								StructuredContent: json.RawMessage(marshalled), // structured (JSON)
							}, nil
						} else {
							return &mcp.CallToolResult{
								Content: []mcp.Content{
									&mcp.TextContent{
										Text: fmt.Sprintf("Failed to marshal cmdline result: %s", err),
									},
								},
								IsError: true,
							}, nil
						}
					}
				} else {
					err = fmt.Errorf("failed to parse 'cmdline': %w", err)
				}
			} else {
				err = fmt.Errorf("failed to get parameter 'cmdline': %w", err)
			}

			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{
						Text: fmt.Sprintf(
							"Failed to execute cmdline '%s': %s",
							*cmdline,
							err,
						),
					},
				},
				IsError: true,
			}, nil
		},
	})
	//
	// do http request
	toolsAndHandlers = append(toolsAndHandlers, toolAndHandler{
		tool: mcp.Tool{
			Name: `gmn_do_http`,
			Description: `This function sends a HTTP request and returns the response.
`,
			Annotations: &mcp.ToolAnnotations{
				DestructiveHint: ptr(true),
			},
			InputSchema: &jsonschema.Schema{
				Type:     "object",
				ReadOnly: true,
				Properties: map[string]*jsonschema.Schema{
					"method": {
						Title:       "method",
						Description: `HTTP request method.`,
						Type:        "string",
						Enum: []any{
							"GET",
							"POST",
							"DELETE",
							"PUT",
						},
					},
					"url": {
						Title:       "url",
						Description: `HTTP request URL.`,
						Type:        "string",
					},
					"headers": {
						Title:       "headers",
						Description: `HTTP request headers. Keys and values are all strings.`,
						Type:        "object",
					},
					"params": {
						Title:       "params",
						Description: `HTTP request parameters, especially for GET/DELETE requests.`,
						Type:        "object",
					},
					"body": {
						Title: "body",
						Description: `HTTP request body, especially for POST/PUT requests.

* NOTE:
Mime type of this parameter should also be specified in the 'Content-Type' header, eg. 'application/json', with the 'headers' parameter.`,
						Type: "string",
					},
				},
				Required: []string{
					"method",
					"url",
				},
			},
		},
		handler: func(
			ctx context.Context,
			request *mcp.CallToolRequest,
		) (result *mcp.CallToolResult, err error) {
			// convert arguments
			var args map[string]any
			if json.Unmarshal(request.Params.Arguments, &args) != nil {
				return &mcp.CallToolResult{
					Content: []mcp.Content{
						&mcp.TextContent{
							Text: fmt.Sprintf(
								"Failed to convert arguments to `%T`: %s",
								args,
								err,
							),
						},
					},
					IsError: true,
				}, nil
			}

			// get 'method'
			var method *string
			if method, err = gt.FuncArg[string](args, "method"); err == nil {
				// get 'url'
				var urlString *string
				if urlString, err = gt.FuncArg[string](args, "url"); err == nil {
					if u, uerr := url.Parse(*urlString); uerr == nil {
						// get 'headers'
						var headers *map[string]any
						if headers, err = gt.FuncArg[map[string]any](args, "headers"); err != nil {
							writer.error("failed to get parameter 'headers': %w", err)
						}

						// get 'params'
						var params *map[string]any
						if params, err = gt.FuncArg[map[string]any](args, "params"); err != nil {
							writer.error("failed to get parameter 'params': %w", err)
						}

						// get 'body'
						var body *string
						if body, err = gt.FuncArg[string](args, "body"); err != nil {
							writer.error("failed to get parameter 'body': %w", err)
						}

						hc := http.DefaultClient
						var req *http.Request
						switch *method {
						case "GET", "DELETE":
							req, err = http.NewRequest(*method, u.String(), nil)
							if params != nil {
								q := req.URL.Query()
								for k, v := range *params {
									q.Add(k, fmt.Sprintf("%v", v))
								}
								req.URL.RawQuery = q.Encode()
							}
						case "POST", "PUT":
							var reader *bytes.Reader
							if body != nil {
								reader = bytes.NewReader([]byte(*body))
							}
							req, err = http.NewRequest(*method, u.String(), reader)
						default:
							return &mcp.CallToolResult{
								Content: []mcp.Content{
									&mcp.TextContent{
										Text: fmt.Sprintf("not a supported 'method' for http request: %s", err),
									},
								},
								IsError: true,
							}, nil
						}

						if err == nil {
							// headers
							if headers != nil {
								for k, v := range *headers {
									req.Header.Set(k, fmt.Sprintf("%v", v))
								}
							}

							var resp *http.Response
							var body []byte
							if resp, err = hc.Do(req); err == nil {
								defer func() { _ = resp.Body.Close() }()

								body, err = io.ReadAll(resp.Body)
							}

							var marshalled []byte
							if marshalled, err = json.Marshal(struct {
								RequestedMethod string `json:"requestedMethod"`
								RequestedURL    string `json:"requestedUrl"`

								Status  int                 `json:"status"`
								Headers map[string][]string `json:"headers,omitempty"`
								Body    string              `json:"body"`
							}{
								RequestedMethod: *method,
								RequestedURL:    *urlString,

								Status:  resp.StatusCode,
								Headers: resp.Header,
								Body:    string(body),
							}); err == nil {
								return &mcp.CallToolResult{
									Content: []mcp.Content{
										&mcp.TextContent{
											Text: string(marshalled),
										},
									},
									StructuredContent: json.RawMessage(marshalled), // structured (JSON)
								}, nil
							} else {
								return &mcp.CallToolResult{
									Content: []mcp.Content{
										&mcp.TextContent{
											Text: fmt.Sprintf("Failed to marshal http response: %s", err),
										},
									},
									IsError: true,
								}, nil
							}
						} else {
							err = fmt.Errorf("failed to do http request: %w", err)
						}
					} else {
						err = fmt.Errorf("invalid url '%s': %w", *urlString, err)
					}
				} else {
					err = fmt.Errorf("failed to get parameter 'url': %w", err)
				}
			} else {
				err = fmt.Errorf("failed to get parameter 'method': %w", err)
			}

			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{
						Text: fmt.Sprintf(
							"Failed to do HTTP request: %s",
							err,
						),
					},
				},
				IsError: true,
			}, nil
		},
	})

	// add tools to server
	tools := []*mcp.Tool{}
	for _, t := range toolsAndHandlers {
		server.AddTool(&t.tool, t.handler)

		tools = append(tools, &t.tool)
	}

	return server, tools
}

// run MCP server through STDIO
func runStdioServer(
	ctx context.Context,
	writer outputWriter,
	conf config,
	p params,
) (err error) {
	vbs := p.Verbose

	server, _ := buildSelfServer(writer, conf, p)

	// trap signals
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	writer.verbose(
		verboseMinimum,
		vbs,
		"connecting to MCP server...",
	)

	// connect,
	if _, err = server.Connect(
		ctx,
		&mcp.StdioTransport{},
		&mcp.ServerSessionOptions{},
	); err != nil {
		if err == context.Canceled {
			writer.verbose(
				verboseNone,
				vbs,
				"Server context canceled. Exiting.",
			)
			return nil
		} else {
			writer.verbose(
				verboseNone,
				vbs,
				"Server connection error: %v", err,
			)
			return fmt.Errorf("server connection error: %w", err)
		}
	}

	// wait for signal
	writer.verbose(
		verboseNone,
		vbs,
		"Server waiting for explicit shutdown signal (Ctrl+C / SIGTERM)...",
	)
	<-ctx.Done()
	writer.verbose(
		verboseNone,
		vbs,
		"Shutdown signal received: %v", ctx.Err(),
	)

	return nil
}

// return self as a MCP tool for local use (in-memory)
func selfAsMCPTool(
	ctx context.Context,
	conf config,
	p params,
	writer outputWriter,
) (connDetails *mcpConnectionDetails, err error) {
	server, tools := buildSelfServer(writer, conf, p)

	writer.verbose(
		verboseMinimum,
		p.Verbose,
		"connecting to MCP server (self)...",
	)

	var conn *mcp.ClientSession
	if conn, err = mcpRunInMemory(ctx, server); err != nil {
		return nil, fmt.Errorf("failed to run in-memory mcp server (self): %w", err)
	}

	return &mcpConnectionDetails{
		serverType: mcpServerStdio,
		connection: conn,
		tools:      tools,
	}, nil
}
