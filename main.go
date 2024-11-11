package main

import (
	"io"
	"os"
	"strings"

	"github.com/jessevdk/go-flags"
)

const (
	appName = "gmn"
)

// parameter definitions
type params struct {
	// config file's path
	ConfigFilepath *string `short:"c" long:"config" description:"Config file's path (default: $XDG_CONFIG_HOME/gmn/config.json)"`

	// for gemini model
	GoogleAIAPIKey *string `short:"k" long:"api-key" description:"API Key to use (can be ommitted if set in config)"`
	GoogleAIModel  *string `short:"m" long:"model" description:"Model to use (can be omitted)"`

	// prompt and filepaths for generation
	SystemInstruction *string   `short:"s" long:"system" description:"System instruction (can be omitted)"`
	Prompt            *string   `short:"p" long:"prompt" description:"Prompt to use (can also be read from stdin)"`
	Filepaths         []*string `short:"f" long:"filepath" description:"Path of a file or directory (can be used multiple times)"`

	// for fetching contents
	ReplaceHTTPURLsInPrompt bool    `short:"x" long:"convert-urls" description:"Convert URLs in the prompt to their text representations"`
	UserAgent               *string `short:"u" long:"user-agent" description:"Override user-agent when fetching contents from URLs in the prompt"`

	// for cached contexts
	CacheContext        bool    `short:"C" long:"cache-context" description:"Cache things for future generations and print the cached context's name"`
	ListCachedContexts  bool    `short:"L" long:"list-cached-contexts" description:"List all cached contexts"`
	CachedContextName   *string `short:"N" long:"context-name" description:"Name of the cached context to use"`
	DeleteCachedContext *string `short:"D" long:"delete-cached-context" description:"Delete the cached context with given name"`

	// other options
	Verbose []bool `short:"v" long:"verbose" description:"Show verbose logs (can be used multiple times)"`
}

// check if prompt is given in the params
func (p *params) hasPrompt() bool {
	return p.Prompt != nil && len(*p.Prompt) > 0
}

// check if multiple tasks are requested
// FIXME: TODO: need to be fixed when a new task is added
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
	if hasPrompt && !promptCounted { // no other tasks requested, but prompt is given
		num++
	}

	return num > 1
}

// main
func main() {
	// read from standard input, if any
	var stdin []byte
	stat, _ := os.Stdin.Stat()
	if (stat.Mode() & os.ModeCharDevice) == 0 {
		stdin, _ = io.ReadAll(os.Stdin)
	}

	// parse params,
	var p params
	parser := flags.NewParser(&p, flags.HelpFlag|flags.PassDoubleDash)
	if remaining, err := parser.Parse(); err == nil {
		if len(stdin) > 0 {
			if p.Prompt == nil {
				p.Prompt = ptr(string(stdin))
			} else {
				// merge prompts from stdin and parameter
				merged := string(stdin) + "\n\n" + *p.Prompt
				p.Prompt = ptr(merged)

				logVerbose(verboseMedium, p.Verbose, "merged prompt: %s\n\n", merged)
			}
		}

		// check if multiple tasks were requested at a time
		if p.multipleTaskRequested() {
			logMessage(verboseMaximum, "Input error: multiple tasks were requested at a time.")

			printHelpAndExit(1, parser)
		}

		// check if there was any parameter without flag
		if len(remaining) > 0 {
			logMessage(verboseMaximum, "Input error: parameters without flags: %s", strings.Join(remaining, " "))

			printHelpAndExit(1, parser)
		}

		// run with params
		run(parser, p)
	} else {
		if e, ok := err.(*flags.Error); ok {
			helpExitCode := 0
			if e.Type != flags.ErrHelp {
				helpExitCode = 1

				logMessage(verboseMedium, "Input error: %s", e.Error())
			}

			printHelpAndExit(helpExitCode, parser)
		}

		printErrorAndExit("Failed to parse flags: %s\n", err)
	}
}
