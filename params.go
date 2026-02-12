// params.go
//
// Input parameters from command line and their helper functions.

package main

// parameter definitions
type params struct {
	// for showing the version
	ShowVersion bool `long:"version" description:"Show the version of this application"`

	// for listing models
	ListModels bool `short:"l" long:"list-models" description:"List available models"`

	Configuration struct {
		// configuration file's path
		ConfigFilepath *string `short:"c" long:"config" description:"Config file's path (default: $XDG_CONFIG_HOME/gmn/config.json)" value-name:"CONFIG_FILEPATH"`

		// values for credentials
		GoogleAIAPIKey      *string `short:"k" long:"api-key" description:"Google AI API Key (can be ommitted if set in config)" value-name:"GEMINI_API_KEY"`
		CredentialsFilepath *string `short:"K" long:"credentials-filepath" description:"Google credentials filepath (can be ommitted if set in config)" value-name:"GCP_CREDENTIALS_FILEPATH"`

		// for model configuration
		GoogleAIModel *string `short:"m" long:"model" description:"Model for generation (can be omitted)" value-name:"MODEL_NAME"`
	} `group:"Configuration"`

	Generation struct {
		// prompt, system instruction, and other things for generation
		Prompt      *string   `short:"p" long:"prompt" description:"Prompt for generation (can also be read from stdin)"`
		Filepaths   []*string `short:"f" long:"filepath" description:"Path of a file or directory (can be used multiple times)"`
		ThinkingOn  bool      `short:"t" long:"with-thinking" description:"Generate with thinking"`
		GroundingOn bool      `short:"g" long:"with-grounding" description:"Generate with grounding (Google Search)"`

		// other generation options
		OutputAsJSON bool `short:"j" long:"json" description:"Whether to output generated results as JSON"`

		// detailed generation options
		DetailedOptions struct {
			SystemInstruction *string `short:"s" long:"system" description:"System instruction (can be omitted)" value-name:"INSTRUCTION"`

			Temperature *float32 `long:"temperature" description:"'temperature' for generation" value-name:"TEMP"`
			TopP        *float32 `long:"top-p" description:"'top_p' for generation" value-name:"TOP_P"`
			TopK        *int32   `long:"top-k" description:"'top_k' for generation" value-name:"TOP_K"`

			ThinkingLevel *string `long:"thinking-level" description:"Level for thinking ('low', 'medium', 'high', or 'minimal')" value-name:"LEVEL"`
			ShowThinking  bool    `long:"show-thinking" description:"Show thinking process between <thought></thought> tags"`

			FileSearchStores []string `long:"file-search-store" description:"Name of file search store (can be used multiple times)"`

			Seed *int32 `long:"seed" description:"Seed for generation" value-name:"SEED"`
		} `group:"Detailed Generation Options"`

		// google maps
		GoogleMaps struct {
			WithGoogleMaps bool     `long:"with-google-maps" description:"Generate with Google Maps"`
			Latitude       *float64 `long:"google-maps-latitude" description:"Latitude for Google Maps query" value-name:"LAT"`
			Longitude      *float64 `long:"google-maps-longitude" description:"Longitude for Google Maps query" value-name:"LONG"`
		} `group:"Google Maps"`

		// for fetching contents
		FetchContents struct {
			ReplaceHTTPURLsInPrompt bool    `short:"x" long:"convert-urls" description:"Convert URLs in the prompt to their text representations (when not given, URLs will be fetched or reused from cache by Gemini API automatically)"`
			KeepURLsAsIs            bool    `short:"X" long:"keep-urls" description:"Keep URLs in the prompt as-is (when not given, URLs will be fetched or reused from cache by Gemini API automatically)"`
			UserAgent               *string `long:"user-agent" description:"Override user-agent when fetching contents from URLs in the prompt" value-name:"USER_AGENT"`
		} `group:"Options for Fetching Contents"`

		// for image generation
		Image struct {
			GenerateImages bool    `long:"with-images" description:"Generate images if possible (system instruction will be ignored)"`
			SaveToFiles    bool    `long:"save-images" description:"Save generated images to files"`
			SaveToDir      *string `long:"save-images-to-dir" description:"Save generated images to a directory ($TMPDIR when not given)" value-name:"DIR"`
		} `group:"Image Generation"`

		// for video generation
		Video struct {
			GenerateVideos  bool              `long:"with-videos" description:"Generate videos (system instruction will be ignored)"`
			NegativePrompt  *string           `long:"negative-prompt-for-videos" description:"Negative prompt for video generation (can be omitted)" value-name:"PROMPT"`
			ReferenceImages map[string]string `long:"reference-image-for-videos" description:"Reference images for video generation (can be used multiple times, eg. '/path/to/image1.jpg:style', '/path/to/image2.png:asset')"`
			SaveToDir       *string           `long:"save-videos-to-dir" description:"Save generated videos to a directory ($TMPDIR when not given)" value-name:"DIR"`
			NumGenerated    int32             `long:"num-generated-videos" description:"Number of generated videos" default:"1" value-name:"NUM"`
			Resolution      *string           `long:"resolution-for-videos" description:"Resolution for generated videos"  default:"1080p" value-name:"RESOLUTION"`
			DurationSeconds int32             `long:"generated-videos-duration-seconds" description:"Duration of generated videos in seconds" default:"8" value-name:"DURATION"`
			FPS             int32             `long:"generated-videos-fps" description:"Frames per second for generated videos" default:"24" value-name:"FPS"`
		} `group:"Video Generation"`

		// for speech generation
		Speech struct {
			GenerateSpeech bool              `long:"with-speech" description:"Generate speeches (system instruction will be ignored)"`
			Language       *string           `long:"speech-language" description:"Language for speech generation in BCP-47 code (eg. 'en-US')" value-name:"LANG"`
			Voice          *string           `long:"speech-voice" description:"Voice name for the generated speech (eg. 'Kore')" value-name:"VOICE"`
			Voices         map[string]string `long:"speech-voices" description:"Voices for speech generation (can be used multiple times, eg. 'Speaker 1:Kore', 'Speaker 2:Puck')"`
			SaveToDir      *string           `long:"save-speech-to-dir" description:"Save generated speech to a directory ($TMPDIR when not given)" value-name:"DIR"`
		} `group:"Speech Generation"`
	} `group:"Generation"`

	// tools
	Tools struct {
		ShowCallbackResults      bool `long:"show-callback-results" description:"Whether to force printing the results of tool callbacks (default: only in verbose mode)"`
		RecurseOnCallbackResults bool `short:"r" long:"recurse-on-callback-results" description:"Whether to do recursive generations on callback results"`
		MaxCallbackLoopCount     int  `long:"max-callback-loop-count" description:"Maximum number of times to call a tool callback with the same arguments" default:"0" value-name:"COUNT"`

		ForceCallDestructiveTools bool `long:"force-call-destructive-tools" description:"Whether to force calling destructive tools without asking"`
	} `group:"Tools"`

	// tools (local)
	LocalTools struct {
		Tools                *string           `long:"tools" description:"Tools for function call (in JSON)" value-name:"JSON"`
		ToolConfig           *string           `long:"tool-config" description:"Tool configuration for function call (in JSON)" value-name:"JSON"`
		ToolCallbacks        map[string]string `long:"tool-callbacks" description:"Tool callbacks (can be used multiple times, eg. 'fn_name1:/path/to/script1.sh', 'fn_name2:/path/to/script2.sh')"`
		ToolCallbacksConfirm map[string]bool   `long:"tool-callbacks-confirm" description:"Confirm before executing tool callbacks (can be used multiple times, eg. 'fn_name1:true', 'fn_name2:false')"`
	} `group:"Tools (Local)"`

	// tools (MCP)
	MCPTools struct {
		StreamableHTTPURLs     []string `long:"mcp-streamable-url" description:"Streamable HTTP URLs of MCP Tools (can be used multiple times)" value-name:"URL"`
		STDIOCommands          []string `long:"mcp-stdio-command" description:"Commands of local stdio MCP Tools (can be used multiple times)" value-name:"CMD"`
		WithSelfAsSTDIOCommand bool     `short:"T" long:"mcp-tool-self" description:"Will add itself as an internal MCP tool"`

		RunAsStandaloneSTDIOServer bool `short:"M" long:"mcp-server-self" description:"Run as a standalone STDIO MCP server"`
	} `group:"Tools (MCP)"`

	// for embedding
	Embeddings struct {
		GenerateEmbeddings            bool    `short:"E" long:"gen-embeddings" description:"Generate embeddings of the prompt"`
		EmbeddingsTaskType            *string `long:"embeddings-task-type" description:"Task type for embeddings" value-name:"TYPE"`
		EmbeddingsChunkSize           *uint   `long:"embeddings-chunk-size" description:"Chunk size for embeddings" default:"4096" value-name:"SIZE"`
		EmbeddingsOverlappedChunkSize *uint   `long:"embeddings-overlapped-chunk-size" description:"Overlapped size of chunks for embeddings" default:"64" value-name:"SIZE"`
	} `group:"Embeddings"`

	// for managing cached contexts
	Caching struct {
		CacheContext        bool    `short:"C" long:"cache-context" description:"Cache things for future generations and print the cached context's name"`
		ListCachedContexts  bool    `short:"L" long:"list-cached-contexts" description:"List all cached contexts"`
		CachedContextName   *string `short:"N" long:"context-name" description:"Name of the cached context to use" value-name:"NAME"`
		DeleteCachedContext *string `short:"D" long:"delete-cached-context" description:"Delete the cached context with given name" value-name:"NAME"`
	} `group:"Caching"`

	// for file search
	FileSearch struct {
		ListFileSearchStores bool `long:"list-file-search-stores" description:"List all file search stores"`

		CreateFileSearchStore *string `long:"create-file-search-store" description:"Display name of a new file search store to create" value-name:"DISPLAY_NAME"`
		DeleteFileSearchStore *string `long:"delete-file-search-store" description:"Name of a file search store to delete" value-name:"NAME"`

		FileSearchStoreNameToUploadFiles *string `long:"upload-to-file-search-store" description:"Name of a file search store to upload files to" value-name:"NAME"`

		ListFilesInFileSearchStore  *string `long:"list-files-in-file-search-store" description:"Name of a file search store to list files in" value-name:"NAME"`
		DeleteFileInFileSearchStore *string `long:"delete-file-in-file-search-store" description:"Name of a file in file search store to delete" value-name:"NAME"`
	} `group:"File Search"`

	// others
	OverrideFileMIMEType map[string]string `long:"override-file-mimetype" description:"Override MIME type for given file's extension (can be used multiple times, eg. '.apk:application/zip', '.md:text/markdown')"`

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
		p.MCPTools.RunAsStandaloneSTDIOServer ||
		p.FileSearch.ListFileSearchStores ||
		p.FileSearch.CreateFileSearchStore != nil ||
		p.FileSearch.DeleteFileSearchStore != nil ||
		p.FileSearch.FileSearchStoreNameToUploadFiles != nil ||
		p.FileSearch.ListFilesInFileSearchStore != nil ||
		p.FileSearch.DeleteFileInFileSearchStore != nil ||
		p.ShowVersion
}

// check if multiple tasks are requested
//
// FIXME: TODO: need to be fixed whenever a new task is added
func (p *params) multipleTasksRequested() bool {
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
	if p.MCPTools.RunAsStandaloneSTDIOServer { // run as a STDIO MCP server
		num++
		if hasPrompt && !promptCounted {
			num++
			promptCounted = true
		}
	}
	if p.FileSearch.ListFileSearchStores { // list file search stores
		num++
		if hasPrompt && !promptCounted {
			num++
			promptCounted = true
		}
	}
	if p.FileSearch.CreateFileSearchStore != nil { // create file search store
		num++
		if hasPrompt && !promptCounted {
			num++
			promptCounted = true
		}
	}
	if p.FileSearch.DeleteFileSearchStore != nil { // delete file search store
		num++
		if hasPrompt && !promptCounted {
			num++
			promptCounted = true
		}
	}
	if p.FileSearch.FileSearchStoreNameToUploadFiles != nil { // upload files to file search store
		num++
		if hasPrompt && !promptCounted {
			num++
			promptCounted = true
		}
	}
	if p.FileSearch.ListFilesInFileSearchStore != nil { // list files in file search store
		num++
		if hasPrompt && !promptCounted {
			num++
			promptCounted = true
		}
	}
	if p.FileSearch.DeleteFileInFileSearchStore != nil { // delete a file in file search store
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

// check if multiple media types are requested
//
// FIXME: TODO: need to be fixed whenever a new media type is added
func (p *params) multipleMediaTypesRequested() bool {
	num := 0

	if p.Generation.Image.GenerateImages { // generate images
		num++
	}
	if p.Generation.Speech.GenerateSpeech { // generate speeches
		num++
	}
	if p.Generation.Video.GenerateVideos { // generate videos
		num++
	}

	return num > 1
}

// redact params for printing to stdout
func (p *params) redact() params {
	copied := *p
	copied.Configuration.GoogleAIAPIKey = new("REDACTED")
	return copied
}
