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

	// output writer
	writer := newOutputWriter()

	// parse params,
	var p params
	parser := flags.NewParser(
		&p,
		flags.HelpFlag|flags.PassDoubleDash,
	)
	if remaining, err := parser.Parse(); err == nil {
		if len(stdin) > 0 {
			if p.Generation.Prompt == nil {
				p.Generation.Prompt = ptr(string(stdin))
			} else {
				// merge prompts from stdin and parameter
				merged := string(stdin) + "\n\n" + *p.Generation.Prompt
				p.Generation.Prompt = ptr(merged)

				writer.verbose(
					verboseMedium,
					p.Verbose,
					"merged prompt: %s\n\n",
					merged,
				)
			}
		}

		// check if multiple tasks were requested at a time
		if p.multipleTaskRequested() {
			writer.print(
				verboseMaximum,
				"Input error: multiple tasks were requested at a time.",
			)

			os.Exit(writer.printHelpBeforeExit(1, parser))
		}

		// check if there was any parameter without flag
		if len(remaining) > 0 {
			writer.print(
				verboseMaximum,
				"Input error: parameters without flags: %s",
				strings.Join(remaining, " "),
			)

			os.Exit(writer.printHelpBeforeExit(1, parser))
		}

		// run with params
		exit, err := run(parser, p, writer)

		if err != nil {
			if gt.IsQuotaExceeded(err) {
				os.Exit(writer.printErrorBeforeExit(
					exit,
					"API quota exceeded, try again later: %s",
					err,
				))
			} else if gt.IsModelOverloaded(err) {
				os.Exit(writer.printErrorBeforeExit(
					exit,
					"Model overloaded, try again later: %s",
					err,
				))
			} else {
				os.Exit(writer.printErrorBeforeExit(
					exit,
					"Error: %s",
					err,
				))
			}
		} else {
			os.Exit(exit)
		}
	} else {
		if e, ok := err.(*flags.Error); ok {
			helpExitCode := 0
			if e.Type != flags.ErrHelp {
				helpExitCode = 1

				writer.print(
					verboseMedium,
					"Input error: %s",
					e.Error(),
				)
			}

			os.Exit(writer.printHelpBeforeExit(
				helpExitCode,
				parser,
			))
		}

		os.Exit(writer.printErrorBeforeExit(
			1,
			"Failed to parse flags: %s",
			err,
		))
	}

	// should not reach here
	os.Exit(writer.printErrorBeforeExit(
		1,
		"Unhandled error.",
	))
}
