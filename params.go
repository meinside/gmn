// params.go
//
// input parameters from command line and their helper functions

package main

// parameter definitions
type params struct {
	// for showing the version
	ShowVersion bool `long:"version" description:"Show the version of this application"`

	// for listing models
	ListModels bool `short:"l" long:"list-models" description:"List available models"`

	Configuration struct {
		// configuration file's path
		ConfigFilepath *string `short:"c" long:"config" description:"Config file's path (default: $XDG_CONFIG_HOME/gmn/config.json)"`

		// for model configuration
		GoogleAIAPIKey *string `short:"k" long:"api-key" description:"Google AI API Key (can be ommitted if set in config)"`
		GoogleAIModel  *string `short:"m" long:"model" description:"Model for generation (can be omitted)"`
	} `group:"Configuration"`

	Generation struct {
		// prompt, system instruction, and other things for generation
		Prompt            *string   `short:"p" long:"prompt" description:"Prompt for generation (can also be read from stdin)"`
		Filepaths         []*string `short:"f" long:"filepath" description:"Path of a file or directory (can be used multiple times)"`
		SystemInstruction *string   `short:"s" long:"system" description:"System instruction (can be omitted)"`
		Temperature       *float32  `long:"temperature" description:"'temperature' for generation (default: 1.0)"`
		TopP              *float32  `long:"top-p" description:"'top_p' for generation (default: 0.95)"`
		TopK              *int32    `long:"top-k" description:"'top_k' for generation (default: 20)"`
		ThinkingOn        bool      `long:"with-thinking" description:"Generate with thinking on"`
		ThinkingBudget    *int32    `long:"thinking-budget" description:"Budget for thinking (default: 1024)"`
		GroundingOn       bool      `short:"g" long:"with-grounding" description:"Generate with grounding"`

		// for fetching contents
		ReplaceHTTPURLsInPrompt bool    `short:"x" long:"convert-urls" description:"Convert URLs in the prompt to their text representations"`
		UserAgent               *string `long:"user-agent" description:"Override user-agent when fetching contents from URLs in the prompt"`

		// other generation options
		Tools                    *string           `long:"tools" description:"Tools for function call (in JSON)"`
		ToolConfig               *string           `long:"tool-config" description:"Tool configuration for function call (in JSON)"`
		ToolCallbacks            map[string]string `long:"tool-callbacks" description:"Tool callbacks (can be used multiple times, eg. 'fn_name1:/path/to/script1.sh', 'fn_name2:/path/to/script2.sh')"`
		ToolCallbacksConfirm     map[string]bool   `long:"tool-callbacks-confirm" description:"Confirm before executing tool callbacks (can be used multiple times, eg. 'fn_name1:true', 'fn_name2:false')"`
		ShowCallbackResults      bool              `long:"show-callback-results" description:"Whether to force print the results of tool callbacks (default: only in verbose mode)"`
		RecurseOnCallbackResults bool              `long:"recurse-on-callback-results" description:"Whether to do recursive generations on callback results (default: false)"`
		OutputAsJSON             bool              `short:"j" long:"json" description:"Output generated results as JSON"`

		// for image generation
		GenerateImages    bool    `long:"with-images" description:"Generate images if possible (system instruction will be ignored)"`
		SaveImagesToFiles bool    `long:"save-images" description:"Save generated images to files"`
		SaveImagesToDir   *string `long:"save-images-to-dir" description:"Save generated images to a directory ($TMPDIR when not given)"`

		// for speech generation
		GenerateSpeech  bool              `long:"with-speech" description:"Generate speeches (system instruction will be ignored)"`
		SpeechLanguage  *string           `long:"speech-language" description:"Language for speech generation in BCP-47 code (eg. 'en-US')"`
		SpeechVoice     *string           `long:"speech-voice" description:"Voice name for the generated speech (eg. 'Kore')"`
		SpeechVoices    map[string]string `long:"speech-voices" description:"Voices for speech generation (can be used multiple times, eg. 'Speaker 1:Kore', 'Speaker 2:Puck')"`
		SaveSpeechToDir *string           `long:"save-speech-to-dir" description:"Save generated speech to a directory ($TMPDIR when not given)"`
	} `group:"Generation"`

	// for embedding
	Embeddings struct {
		GenerateEmbeddings            bool    `short:"E" long:"gen-embeddings" description:"Generate embeddings of the prompt"`
		EmbeddingsTaskType            *string `long:"embeddings-task-type" description:"Task type for embeddings"`
		EmbeddingsChunkSize           *uint   `long:"embeddings-chunk-size" description:"Chunk size for embeddings (default: 4096)"`
		EmbeddingsOverlappedChunkSize *uint   `long:"embeddings-overlapped-chunk-size" description:"Overlapped size of chunks for embeddings (default: 64)"`
	} `group:"Embeddings"`

	// for managing cached contexts
	Caching struct {
		CacheContext        bool    `short:"C" long:"cache-context" description:"Cache things for future generations and print the cached context's name"`
		ListCachedContexts  bool    `short:"L" long:"list-cached-contexts" description:"List all cached contexts"`
		CachedContextName   *string `short:"N" long:"context-name" description:"Name of the cached context to use"`
		DeleteCachedContext *string `short:"D" long:"delete-cached-context" description:"Delete the cached context with given name"`
	} `group:"Caching"`

	// for logging and debugging
	Verbose                []bool `short:"v" long:"verbose" description:"Show verbose logs (can be used multiple times)"`
	ErrorOnUnsupportedType bool   `long:"error-on-unsupported-type" description:"Exit with error when unsupported type of stream is received"`
}

// check if prompt is given in the params
func (p *params) hasPrompt() bool {
	return p.Generation.Prompt != nil && len(*p.Generation.Prompt) > 0
}

// check if any task is requested
func (p *params) taskRequested() bool {
	return p.hasPrompt() ||
		p.Caching.CacheContext ||
		p.Caching.ListCachedContexts ||
		p.Caching.DeleteCachedContext != nil ||
		p.ListModels ||
		p.ShowVersion
}

// check if multiple tasks are requested
//
// FIXME: TODO: need to be fixed whenever a new task is added
func (p *params) multipleTaskRequested() bool {
	hasPrompt := p.hasPrompt()
	promptCounted := false
	num := 0

	if p.Caching.CacheContext { // cache context
		num++
		if hasPrompt && !promptCounted {
			promptCounted = true
		}
	}
	if p.Caching.ListCachedContexts { // list cached contexts
		num++
		if hasPrompt && !promptCounted {
			num++
			promptCounted = true
		}
	}
	if p.Caching.DeleteCachedContext != nil { // delete cached context
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
	if p.ShowVersion { // show version
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
	copied.Configuration.GoogleAIAPIKey = ptr("REDACTED")
	return copied
}
