// logging.go
//
// Things for logging messages.

package main

import (
	"bytes"
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

// output writer interface for printing things
type outputWriter interface {
	endsWithNewline() bool                                                              // check if output ends with a new line
	makeSureToEndWithNewline()                                                          // make sure output ends with a new line
	println()                                                                           // force add a new line to output
	printWithColorForLevel(level verbosity, format string, a ...any)                    // print given string to output with predefined color (will add a new line if there isn't)
	errorWithColorForLevel(level verbosity, format string, a ...any)                    // print given string to error output with predefined color (will add a new line if there isn't)
	printColored(c color.Attribute, format string, a ...any)                            // print given string to output with color (if possible)
	errorColored(c color.Attribute, format string, a ...any)                            // print given string to error output with color (if possible)
	verbose(targetLevel verbosity, verbosityFromParams []bool, format string, a ...any) // print given verbose string to error output (will add a new line if there isn't)
	warn(format string, a ...any)                                                       // print given warning string to error output (will add a new line if there isn't)
	error(format string, a ...any)                                                      // print given error string to error output (will add a new line if there isn't)
	printHelpBeforeExit(code int, parser *flags.Parser) int                             // print help message to error output before os.Exit()
	printErrorBeforeExit(code int, format string, a ...any) int                         // print error string to error output before os.Exit()
}

// output writer for printing to stdout/stderr
type stdoutWriter struct {
	didEndWithNewline bool
}

// generate a new stdoutWriter
func newStdoutWriter() outputWriter {
	return &stdoutWriter{
		didEndWithNewline: true,
	}
}

// check if stdout ends with a new line
func (w *stdoutWriter) endsWithNewline() bool {
	return w.didEndWithNewline
}

// force add a new line to stdout
func (w *stdoutWriter) println() {
	_, _ = fmt.Fprintf(os.Stdout, "\n")

	w.didEndWithNewline = true
}

// make sure stdout ends with new line
func (w *stdoutWriter) makeSureToEndWithNewline() {
	if !w.didEndWithNewline {
		w.println()
	}
}

// print given string to stdout with color (if possible)
func (w *stdoutWriter) printColored(
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

	w.didEndWithNewline = strings.HasSuffix(formatted, "\n")
}

// print given string to stderr with color (if possible)
func (w *stdoutWriter) errorColored(
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

	w.didEndWithNewline = strings.HasSuffix(formatted, "\n")
}

// print given string to stdout with predefined color (will add a new line if there isn't)
func (w *stdoutWriter) printWithColorForLevel(
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

// print given string to stderr with predefined color (will add a new line if there isn't)
func (w *stdoutWriter) errorWithColorForLevel(
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

// print given string to stderr and append a new line if there isn't
func (w *stdoutWriter) errWithNewlineAppended(
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

// print verbose message to stderr (will add a new line if there isn't)
//
// (only when the level of given `verbosityFromParams` is greater or equal to `targetLevel`)
func (w *stdoutWriter) verbose(
	targetLevel verbosity,
	verbosityFromParams []bool,
	format string,
	a ...any,
) {
	if vb := verboseLevel(verbosityFromParams); vb >= targetLevel {
		format = fmt.Sprintf(">>> %s", format)

		w.errorWithColorForLevel(
			targetLevel,
			format,
			a...,
		)
	}
}

// print given warning string to stderr (will add a new line if there isn't)
func (w *stdoutWriter) warn(
	format string,
	a ...any,
) {
	w.errWithNewlineAppended(color.FgMagenta, format, a...)
}

// print given error string to stderr (will add a new line if there isn't)
func (w *stdoutWriter) error(
	format string,
	a ...any,
) {
	w.errWithNewlineAppended(color.FgRed, format, a...)
}

// print help message to stderr before os.Exit()
func (w *stdoutWriter) printHelpBeforeExit(
	code int,
	parser *flags.Parser,
) (exit int) {
	parser.WriteHelp(os.Stderr)

	return code
}

// print error to stderr before os.Exit()
func (w *stdoutWriter) printErrorBeforeExit(
	code int,
	format string,
	a ...any,
) (exit int) {
	if code > 0 {
		w.error(format, a...)
	}

	return code
}

// sprintf given string with color (if possible)
func colorizef(
	c color.Attribute,
	format string,
	a ...any,
) string {
	formatted := fmt.Sprintf(format, a...)

	if supportscolor.Stdout().SupportsColor { // if color is supported,
		buf := new(bytes.Buffer)
		_, _ = color.New(c).Fprint(buf, formatted)
		return buf.String()
	} else {
		return fmt.Sprint(formatted)
	}
}
