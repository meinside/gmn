// params.go
//
// input parameters from command line and their helper functions

package main

// parameter definitions
type params struct {
	// config file's path
	ConfigFilepath *string `short:"c" long:"config" description:"Config file's path (default: $XDG_CONFIG_HOME/gmn/config.json)"`

	// for gemini model
	GoogleAIAPIKey               *string `short:"k" long:"api-key" description:"Google AI API Key (can be ommitted if set in config)"`
	GoogleAIModel                *string `short:"m" long:"model" description:"Model for text generation (can be omitted)"`
	GoogleAIImageGenerationModel *string `short:"i" long:"image-generation-model" description:"Model for image generation (can be omitted)"`
	GoogleAIEmbeddingsModel      *string `short:"b" long:"embeddings-model" description:"Model for embeddings (can be omitted)"`

	// system instruction, prompt, and other things for generation
	SystemInstruction *string   `short:"s" long:"system" description:"System instruction (can be omitted)"`
	Temperature       *float32  `long:"temperature" description:"'temperature' for generation (default: 1.0)"`
	TopP              *float32  `long:"top-p" description:"'top_p' for generation (default: 0.95)"`
	TopK              *int32    `long:"top-k" description:"'top_k' for generation (default: 20)"`
	Prompt            *string   `short:"p" long:"prompt" description:"Prompt for generation (can also be read from stdin)"`
	Filepaths         []*string `short:"f" long:"filepath" description:"Path of a file or directory (can be used multiple times)"`
	ThinkingOn        bool      `long:"with-thinking" description:"Generate with thinking on"`
	ThinkingBudget    *int32    `long:"thinking-budget" description:"Budget for thinking (default: 1024)"`

	// for embedding
	GenerateEmbeddings            bool  `short:"e" long:"gen-embeddings" description:"Generate embeddings of the prompt"`
	EmbeddingsChunkSize           *uint `long:"embeddings-chunk-size" description:"Chunk size for embeddings (default: 4096)"`
	EmbeddingsOverlappedChunkSize *uint `long:"embeddings-overlapped-chunk-size" description:"Overlapped size of chunks for embeddings (default: 64)"`

	// for fetching contents
	ReplaceHTTPURLsInPrompt bool    `short:"x" long:"convert-urls" description:"Convert URLs in the prompt to their text representations"`
	UserAgent               *string `long:"user-agent" description:"Override user-agent when fetching contents from URLs in the prompt"`

	// for cached contexts
	CacheContext        bool    `short:"C" long:"cache-context" description:"Cache things for future generations and print the cached context's name"`
	ListCachedContexts  bool    `short:"L" long:"list-cached-contexts" description:"List all cached contexts"`
	CachedContextName   *string `short:"N" long:"context-name" description:"Name of the cached context to use"`
	DeleteCachedContext *string `short:"D" long:"delete-cached-context" description:"Delete the cached context with given name"`

	// for listing models
	ListModels bool `long:"list-models" description:"List available models"`

	// other options
	OutputAsJSON      bool    `short:"j" long:"json" description:"Output generated results as JSON"`
	GenerateImages    bool    `long:"with-images" description:"Generate images if possible (system instruction will be ignored)"`
	SaveImagesToFiles bool    `long:"save-images" description:"Save generated images to files"`
	SaveImagesToDir   *string `long:"save-images-to-dir" description:"Save generated images to a directory ($TMPDIR when not given)"`

	// for logging and debugging
	ErrorOnUnsupportedType bool   `long:"error-on-unsupported-type" description:"Exit with error when unsupported type of stream is received"`
	Verbose                []bool `short:"v" long:"verbose" description:"Show verbose logs (can be used multiple times)"`
}

// check if prompt is given in the params
func (p *params) hasPrompt() bool {
	return p.Prompt != nil && len(*p.Prompt) > 0
}

// check if any task is requested
func (p *params) taskRequested() bool {
	return p.hasPrompt() || p.CacheContext || p.ListCachedContexts || p.DeleteCachedContext != nil || p.ListModels
}

// check if multiple tasks are requested
//
// FIXME: TODO: need to be fixed whenever a new task is added
func (p *params) multipleTaskRequested() bool {
	hasPrompt := p.hasPrompt()
	promptCounted := false
	num := 0

	if p.CacheContext { // cache context
		num++
		if hasPrompt && !promptCounted {
			promptCounted = true
		}
	}
	if p.ListCachedContexts { // list cached contexts
		num++
		if hasPrompt && !promptCounted {
			num++
			promptCounted = true
		}
	}
	if p.DeleteCachedContext != nil { // delete cached context
		num++
		if hasPrompt && !promptCounted {
			num++
			promptCounted = true
		}
	}
	if p.ListModels { // list models
		num++
		if hasPrompt && !promptCounted {
			num++
			promptCounted = true
		}
	}
	if hasPrompt && !promptCounted { // no other tasks requested, but prompt is given
		num++
	}

	return num > 1
}

// redact params for printing to stdout
func (p *params) redact() params {
	copied := *p
	copied.GoogleAIAPIKey = ptr("REDACTED")
	return copied
}
