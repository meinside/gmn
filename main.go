// main.go
//
// Entry point of this application.

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
	// output writer
	writer := newStdoutWriter()

	// parse params,
	var p params
	parser := flags.NewParser(
		&p,
		flags.HelpFlag|flags.PassDoubleDash,
	)
	if remaining, err := parser.Parse(); err == nil {
		// check if multiple tasks were requested at a time
		if p.multipleTasksRequested() {
			writer.printWithColorForLevel(
				verboseMaximum,
				"Input error: multiple tasks were requested at a time.",
			)

			os.Exit(writer.printHelpBeforeExit(1, parser))
		}

		// check if multiple media types were requested at a time
		if p.multipleMediaTypesRequested() {
			writer.printWithColorForLevel(
				verboseMaximum,
				"Input error: multiple media types were requested at a time.",
			)

			os.Exit(writer.printHelpBeforeExit(1, parser))

		}

		// check if there was any parameter without flag
		if len(remaining) > 0 {
			writer.printWithColorForLevel(
				verboseMaximum,
				"Input error: parameters without flags: %s",
				strings.Join(remaining, " "),
			)

			os.Exit(writer.printHelpBeforeExit(1, parser))
		}

		if p.MCPTools.RunAsStandaloneSTDIOServer { // run as a MCP server?
			// then serve as a MCP server
			exit, err := serve(writer, p)
			if err != nil {
				os.Exit(writer.printErrorBeforeExit(
					exit,
					"Error: %s",
					err,
				))
			} else {
				os.Exit(exit)
			}
		} else { // else,
			// read from standard input, if any
			var stdin []byte
			stat, _ := os.Stdin.Stat()
			if (stat.Mode() & os.ModeCharDevice) == 0 {
				stdin, _ = io.ReadAll(os.Stdin)
			}
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

			// run with params
			exit, err := run(parser, writer, p)

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

			// should not reach here
			os.Exit(writer.printErrorBeforeExit(
				1,
				"Unhandled error.",
			))
		}
	} else {
		if e, ok := err.(*flags.Error); ok {
			helpExitCode := 0
			if e.Type != flags.ErrHelp {
				helpExitCode = 1

				writer.printWithColorForLevel(
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
}
