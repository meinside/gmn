// main.go
//
// entry point of this application

package main

import (
	"io"
	"os"
	"strings"

	"github.com/jessevdk/go-flags"
	gt "github.com/meinside/gemini-things-go"
)

const (
	appName = "gmn"
)

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

			os.Exit(printHelpBeforeExit(1, parser))
		}

		// check if there was any parameter without flag
		if len(remaining) > 0 {
			logMessage(verboseMaximum, "Input error: parameters without flags: %s", strings.Join(remaining, " "))

			os.Exit(printHelpBeforeExit(1, parser))
		}

		// run with params
		exit, err := run(parser, p)

		if err != nil {
			if gt.IsQuotaExceeded(err) {
				os.Exit(printErrorBeforeExit(exit, "API quota exceeded, try again later: %s", err))
			} else {
				os.Exit(printErrorBeforeExit(exit, "Error: %s", err))
			}
		} else {
			os.Exit(exit)
		}
	} else {
		if e, ok := err.(*flags.Error); ok {
			helpExitCode := 0
			if e.Type != flags.ErrHelp {
				helpExitCode = 1

				logMessage(verboseMedium, "Input error: %s", e.Error())
			}

			os.Exit(printHelpBeforeExit(helpExitCode, parser))
		}

		os.Exit(printErrorBeforeExit(1, "Failed to parse flags: %s", err))
	}

	// should not reach here
	os.Exit(printErrorBeforeExit(1, "Unhandled error."))
}
