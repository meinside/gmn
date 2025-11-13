// serve.go
//
// Things for serving a local STDIO MCP server.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gabriel-vasile/mimetype"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/genai"

	gt "github.com/meinside/gemini-things-go"
	"github.com/meinside/version-go"
)

const (
	mcpFunctionTimeoutSeconds = 60

	commandTimeoutSeconds = 30
)

// serve MCP server with params
func serve(
	p params,
	writer *outputWriter,
) (exit int, err error) {
	writer.verbose(
		verboseMinimum,
		p.Verbose,
		"starting MCP server...",
	)

	// read and apply configs
	var conf config
	if conf, err = readConfig(resolveConfigFilepath(p.Configuration.ConfigFilepath)); err != nil {
		// check if environment variable for api key exists,
		if envAPIKey, exists := os.LookupEnv(envVarNameAPIKey); exists {
			// use it,
			p.Configuration.GoogleAIAPIKey = &envAPIKey
		} else {
			// or return an error
			return 1, fmt.Errorf(
				"failed to read configuration: %w",
				err,
			)
		}
	}

	// override command arguments with values from configs
	if conf.GoogleAIAPIKey != nil && p.Configuration.GoogleAIAPIKey == nil {
		p.Configuration.GoogleAIAPIKey = conf.GoogleAIAPIKey
	}

	// set default values
	if p.Generation.UserAgent == nil {
		p.Generation.UserAgent = ptr(defaultFetchUserAgent)
	}

	// check existence of essential parameters here
	if conf.GoogleAIAPIKey == nil && p.Configuration.GoogleAIAPIKey == nil {
		return 1, fmt.Errorf("google AI API Key is missing")
	}

	// files are not supported
	if len(p.Generation.Filepaths) > 0 {
		return 1, fmt.Errorf("files are not supported")
	}

	// run stdio MCP server
	if err = runStdioServer(
		context.TODO(),
		conf,
		p,
		writer,
		p.Verbose,
	); err != nil {
		return 1, err
	}
	return 0, nil
}

// build a MCP server with itself
func buildSelfServer(
	conf config,
	p params,
	writer *outputWriter,
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
			p := p // copy launch params

			var gtc *gt.Client
			gtc, err = gt.NewClient(
				*p.Configuration.GoogleAIAPIKey,
				gt.WithTimeoutSeconds(mcpFunctionTimeoutSeconds),
			)
			if err == nil {
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
			Description: `This function generates texts/images/speeches by processing the given 'prompt'.

If there was any newly-created file, make sure to report to the user about the file's absolute filepath so the user could use it later.
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
						Description: `Paths to local files to be processed along with the given 'prompt'. If a path is not absolute, it will be resolved against the current working directory of this MCP server.`,
						Type:        "array",
					},
					"modality": {
						Title:       "modality",
						Description: `The modality of the generation. Must be one of 'text', 'image', or 'audio'.`,
						Type:        "string",
						Enum: []any{
							"text",
							"image",
							"audio",
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
					var responseModalities []genai.Modality = nil

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
					}

					// get system instruction,
					p.Generation.SystemInstruction = nil
					switch *modality {
					case "text":
						if p.Generation.SystemInstruction == nil {
							if conf.SystemInstruction != nil {
								p.Generation.SystemInstruction = conf.SystemInstruction
							} else {
								p.Generation.SystemInstruction = ptr(defaultSystemInstruction())
							}
						}
					}

					// get appropriate response modalities,
					switch *modality {
					case "image":
						responseModalities = []genai.Modality{
							genai.ModalityText,
							genai.ModalityImage,
						}
					case "audio":
						responseModalities = []genai.Modality{
							genai.ModalityAudio,
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
					gtc, err = gt.NewClient(
						*p.Configuration.GoogleAIAPIKey,
						gt.WithTimeoutSeconds(mcpFunctionTimeoutSeconds),
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
									prompts = append(prompts, gt.PromptFromURI(customURL.url()))
								}
							}
						} else { // (just use the original prompt)
							prompts = append(prompts, gt.PromptFromText(*prompt))
						}

						// read bytes from url prompts and local files, and append them as prompts
						if files, err := openFilesForPrompt(
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

						// generate,
						var res *genai.GenerateContentResponse
						if res, err = gtc.Generate(
							ctx,
							prompts,
							&gt.GenerationOptions{
								Tools:              tools,
								ThinkingOn:         *thinkingOn,
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
											"data[%d]: %d bytes (%s)", i, len(bytes), mimeType,
										)

										if strings.HasPrefix(part.InlineData.MIMEType, "image/") {
											content = append(
												content,
												&mcp.TextContent{
													Text: fmt.Sprintf(
														"Here is the generated image file (%d bytes, %s):",
														len(bytes),
														mimeType,
													),
												},
												&mcp.ImageContent{
													Data:     bytes,
													MIMEType: mimeType,
												},
											)
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
														"Here is the generated audio file (%d bytes, %s):",
														len(bytes),
														mimeType,
													),
												},
												&mcp.AudioContent{
													Data:     bytes,
													MIMEType: mimeType,
												},
											)
										} else {
											writer.err(
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
	// get current working directory (readonly, idempotent, destructive)
	toolsAndHandlers = append(toolsAndHandlers, toolAndHandler{
		tool: mcp.Tool{
			Name: `gmn_get_cwd`,
			Description: `This function returns the current working directory (absolute path).

It is advised to call this function before performing any task which handles filepaths.
`,
			InputSchema: &jsonschema.Schema{
				Type:     "object",
				ReadOnly: true,
			},
			Annotations: &mcp.ToolAnnotations{
				DestructiveHint: ptr(true),
				IdempotentHint:  true,
				ReadOnlyHint:    true,
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
	// stat a file at given path (readonly, destructive)
	toolsAndHandlers = append(toolsAndHandlers, toolAndHandler{
		tool: mcp.Tool{
			Name: `gmn_stat_file`,
			Description: `This function returns the state of a file or directory.

It is advised to call this function before accessing or handling files and/or directories.
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
	// get mime type of a file at given path (readonly, destructive)
	toolsAndHandlers = append(toolsAndHandlers, toolAndHandler{
		tool: mcp.Tool{
			Name: `gmn_get_mimetype`,
			Description: `This function returns the mime type of a file at given path.

It is advised to call this function before reading a file.
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
	// list files at path (readonly, destructive)
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

Make sure to report to the user if this function was called and the specified file was successfully read.
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
There should not be an existing file at the given path.
And this function should not be used for creating binary files due to the risk of file corruption.

Make sure to report to the user if this function was called and the specified file was successfully created.
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

Make sure to report to the user if this function was called and the specified file was successfully deleted.
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

Make sure to report to the user if this function was called and the specified file was successfully moved.
`,
			InputSchema: &jsonschema.Schema{
				Type:     "object",
				ReadOnly: true,
				Properties: map[string]*jsonschema.Schema{
					"fromFilepath": {
						Title:       "fromFilepath",
						Description: `An original path (absolute) of a file that will be moved.`,
						Type:        "string",
					},
					"toFilepath": {
						Title:       "toFilepath",
						Description: `A destination path (absolute) of a moved file.`,
						Type:        "string",
					},
				},
				Required: []string{
					"fromFilepath",
					"toFilepath",
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

			// get 'fromFilepath',
			var fromFilepath *string
			fromFilepath, err = gt.FuncArg[string](args, "fromFilepath")
			if err == nil {
				var toFilepath *string
				toFilepath, err = gt.FuncArg[string](args, "toFilepath")
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
					err = fmt.Errorf("failed to get parameter 'toFilepath': %w", err)
				}
			} else {
				err = fmt.Errorf("failed to get parameter 'fromFilepath': %w", err)
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
The commandline must be in one line, and should be escaped correctly.
Never pass malicious input or non-existing commands to this function, as it will be executed as a shell command.
This function will fail with timeout if the commandline takes %d seconds or longer to finish.
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
	conf config,
	p params,
	writer *outputWriter,
	vbs []bool,
) (err error) {
	server, _ := buildSelfServer(conf, p, writer)

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
	writer *outputWriter,
) (connDetails *mcpConnectionDetails, err error) {
	server, tools := buildSelfServer(conf, p, writer)

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
