// logging.go
//
// Things for logging messages.

package main

import (
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

// output writer for managing stdout
type outputWriter struct {
	endsWithNewLine bool
}

// generate a new output writer
func newOutputWriter() *outputWriter {
	return &outputWriter{
		endsWithNewLine: true,
	}
}

// force add a new line to stdout
func (w *outputWriter) println() {
	_, _ = fmt.Fprintf(os.Stdout, "\n")

	w.endsWithNewLine = true
}

// make sure stdout ends with new line
func (w *outputWriter) makeSureToEndWithNewLine() {
	if !w.endsWithNewLine {
		w.println()
	}
}

// print given string to stdout with color (if possible)
func (w *outputWriter) printColored(
	c color.Attribute,
	format string,
	a ...any,
) {
	formatted := fmt.Sprintf(format, a...)

	if supportscolor.Stdout().SupportsColor { // if color is supported,
		_, _ = color.New(c).Fprint(os.Stdout, formatted)
	} else {
		fmt.Print(formatted)
	}

	w.endsWithNewLine = strings.HasSuffix(formatted, "\n")
}

// print given string to stderr with color (if possible)
func (w *outputWriter) errorColored(
	c color.Attribute,
	format string,
	a ...any,
) {
	formatted := fmt.Sprintf(format, a...)

	if supportscolor.Stderr().SupportsColor { // if color is supported,
		_, _ = color.New(c).Fprint(os.Stderr, formatted)
	} else {
		_, _ = fmt.Fprint(os.Stderr, formatted)
	}

	w.endsWithNewLine = strings.HasSuffix(formatted, "\n")
}

// print given string to stdout (will add a new line if there isn't)
func (w *outputWriter) print(
	level verbosity,
	format string,
	a ...any,
) {
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

	w.printColored(
		c,
		format,
		a...,
	)
}

// print given string to stderr (will add a new line if there isn't)
func (w *outputWriter) err(
	level verbosity,
	format string,
	a ...any,
) {
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

	w.errorColored(
		c,
		format,
		a...,
	)
}

// print verbose message to stderr (will add a new line if there isn't)
//
// (only when the level of given `verbosityFromParams` is greater or equal to `targetLevel`)
func (w *outputWriter) verbose(
	targetLevel verbosity,
	verbosityFromParams []bool,
	format string,
	a ...any,
) {
	if vb := verboseLevel(verbosityFromParams); vb >= targetLevel {
		format = fmt.Sprintf(">>> %s", format)

		w.err(
			targetLevel,
			format,
			a...,
		)
	}
}

// print given string to stderr and append a new line if there isn't
func (w *outputWriter) errWithNewlineAppended(
	c color.Attribute,
	format string,
	a ...any,
) {
	if !strings.HasSuffix(format, "\n") {
		format += "\n"
	}

	w.errorColored(
		c,
		format,
		a...,
	)
}

// print given warning string to stderr (will add a new line if there isn't)
func (w *outputWriter) warn(
	format string,
	a ...any,
) {
	w.errWithNewlineAppended(color.FgMagenta, format, a...)
}

// print given error string to stderr (will add a new line if there isn't)
func (w *outputWriter) error(
	format string,
	a ...any,
) {
	w.errWithNewlineAppended(color.FgRed, format, a...)
}

// print help message to stderr before os.Exit()
func (w *outputWriter) printHelpBeforeExit(
	code int,
	parser *flags.Parser,
) (exit int) {
	parser.WriteHelp(os.Stderr)

	return code
}

// print error to stderr before os.Exit()
func (w *outputWriter) printErrorBeforeExit(
	code int,
	format string,
	a ...any,
) (exit int) {
	if code > 0 {
		w.error(format, a...)
	}

	return code
}
