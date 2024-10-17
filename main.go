package main

import (
	"io"
	"os"

	"github.com/jessevdk/go-flags"
)

// parameter definitions
type params struct {
	// config file's path
	ConfigFilepath *string `short:"c" long:"config" description:"Config file's path (default: $XDG_CONFIG_HOME/gmn/config.json)"`

	// prompt and filepaths for generation
	Prompt            string    `short:"p" long:"prompt" description:"Prompt to use (can also be read from stdin)" required:"true"`
	Filepaths         []*string `short:"f" long:"filepath" description:"Path(s) of file(s)"`
	CacheContext      bool      `short:"C" long:"cache-context" description:"Cache things for future generations and print the cached context's name"`
	CachedContextName *string   `short:"n" long:"context-name" description:"Name of the cached context"`

	// for gemini model
	GoogleAIAPIKey    *string `short:"k" long:"api-key" description:"API Key to use (can be ommitted if set in config)"`
	GoogleAIModel     *string `short:"m" long:"model" description:"Model to use (can be omitted)"`
	SystemInstruction *string `short:"s" long:"system" description:"System instruction (can be omitted)"`

	// for fetching contents
	ReplaceHTTPURLsInPrompt bool    `short:"x" long:"convert-urls" description:"Convert URLs in the prompt to their text representation"`
	UserAgent               *string `short:"u" long:"user-agent" description:"Override user-agent when fetching contents from URLs in the prompt"`

	// other options
	Verbose []bool `short:"v" long:"verbose" description:"Show verbose logs"`
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
	if _, err := parser.Parse(); err == nil {
		if len(stdin) > 0 { // if `prompt` is given from both standard input and parameter, warn the user about it
			logMessage(verboseMedium, "Warning: `prompt` is given from both standard input and parameter; using the parameter.")
		}

		run(p)
	} else {
		if e, ok := err.(*flags.Error); ok {
			if e.Type == flags.ErrRequired { // when required parameter (`prompt`) is missing,
				if len(stdin) > 0 { // when `prompt` is given from standard input, use it
					p.Prompt = string(stdin)

					// run with the params
					run(p)
				} else {
					printHelpAndExit(parser)
				}
			} else if e.Type == flags.ErrHelp { // for help,
				printHelpAndExit(parser)
			}
		}

		printErrorAndExit("Failed to parse flags: %s\n", err)
	}
}

// print help message and exit(1)
func printHelpAndExit(parser *flags.Parser) {
	parser.WriteHelp(os.Stdout)
	os.Exit(1)
}

// print error and exit(1)
func printErrorAndExit(format string, a ...any) {
	logMessage(verboseMaximum, format, a...)
	os.Exit(1)
}
