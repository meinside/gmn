// logging.go
//
// things for logging messages

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/fatih/color"
	"github.com/jessevdk/go-flags"
	"github.com/jwalton/go-supportscolor"
)

// verbosity level constants
type verbosity uint

const (
	verboseNone    verbosity = iota
	verboseMinimum verbosity = iota
	verboseMedium  verbosity = iota
	verboseMaximum verbosity = iota
)

// check level of verbosity
func verboseLevel(verbosityFromParams []bool) verbosity {
	if len(verbosityFromParams) == 1 {
		return verboseMinimum
	} else if len(verbosityFromParams) == 2 {
		return verboseMedium
	} else if len(verbosityFromParams) >= 3 {
		return verboseMaximum
	}

	return verboseNone
}

// print given string to stdout
func logMessage(level verbosity, format string, v ...any) {
	if !strings.HasSuffix(format, "\n") {
		format += "\n"
	}

	var c color.Attribute
	switch level {
	case verboseMinimum:
		c = color.FgGreen
	case verboseMedium, verboseMaximum:
		c = color.FgYellow
	default:
		c = color.FgWhite
	}

	if supportscolor.Stdout().SupportsColor { // if color is supported,
		c := color.New(c)
		_, _ = c.Printf(format, v...)
	} else {
		fmt.Printf(format, v...)
	}
}

// print given error string to stdout
func logError(format string, v ...any) {
	if !strings.HasSuffix(format, "\n") {
		format += "\n"
	}

	if supportscolor.Stdout().SupportsColor { // if color is supported,
		c := color.New(color.FgRed)
		_, _ = c.Printf(format, v...)
	} else {
		fmt.Printf(format, v...)
	}
}

// print logVerbose message
//
// (only when the level of given `verbosityFromParams` is greater or equal to `targetLevel`)
func logVerbose(targetLevel verbosity, verbosityFromParams []bool, format string, v ...any) {
	if vb := verboseLevel(verbosityFromParams); vb >= targetLevel {
		format = fmt.Sprintf(">>> %s", format)

		logMessage(targetLevel, format, v...)
	}
}

// print help message before os.Exit()
func printHelpBeforeExit(code int, parser *flags.Parser) (exit int) {
	parser.WriteHelp(os.Stdout)

	return code
}

// print error before os.Exit()
func printErrorBeforeExit(code int, format string, a ...any) (exit int) {
	if code > 0 {
		logError(format, a...)
	}

	return code
}

// prettify given thing in JSON format
func prettify(v any) string {
	if bytes, err := json.MarshalIndent(v, "", "  "); err == nil {
		return string(bytes)
	}
	return fmt.Sprintf("%+v", v)
}
