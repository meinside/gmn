// helpers_test.go
//
// Things for testing `helpers.go`.

package main

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"
)

// test `expandPath` with various paths
func TestExpandPath(t *testing.T) {
	type test struct {
		input  string
		output string
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Errorf("failed to get home directory: %s", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Errorf("failed to get current working directory: %s", err)
	}

	tests := []test{
		// should handle '~' correctly
		{
			input:  "~/tmp",
			output: homeDir + "/tmp",
		},
		{
			input:  "./test",
			output: cwd + "/test",
		},
		// should handle environment variables correctly
		{
			input:  "$HOME/tmp",
			output: homeDir + "/tmp",
		},
		// should handle relative paths correctly
		{
			input:  "~/tmp/a/b/../..",
			output: homeDir + "/tmp",
		},
	}

	for _, test := range tests {
		output := expandPath(test.input)
		if output != test.output {
			t.Errorf("expected '%s', got '%s'", test.output, output)
		}
	}
}

// test `parseCommandline` with various commandlines
func TestCommandlineParsing(t *testing.T) {
	type test struct {
		cmdline string
		parsed  []string
	}

	tests := []test{
		// should work with single/double quotes, multiline, etc.
		{
			cmdline: `/path/to/executable --text "testin' commandline parsing" \
--phrase '"should work" correctly' -v`,
			parsed: []string{
				`/path/to/executable`,
				`--text`,
				`"testin' commandline parsing"`,
				`--phrase`,
				`'"should work" correctly'`,
				`-v`,
			},
		},
	}

	for _, test := range tests {
		cmdline, args, err := parseCommandline(test.cmdline)
		if err != nil {
			t.Errorf("failed to parse commandline '%s': %s", test.cmdline, err)
		}

		merged := append([]string{cmdline}, args...)
		if !slices.Equal(merged, test.parsed) {
			t.Errorf("expected '%s', got '%s'", prettify(test.parsed, true), prettify(merged, true))
		}
	}
}

// test `runShellCommandWithContext` with shell features (pipes, redirections,
// logical operators, variable expansion, etc.)
func TestRunShellCommandWithContext(t *testing.T) {
	type test struct {
		cmdline  string
		stdout   string
		exitCode int
	}

	tests := []test{
		// plain command + args
		{
			cmdline:  `echo hello world`,
			stdout:   "hello world\n",
			exitCode: 0,
		},
		// pipe
		{
			cmdline:  `printf 'foo\nbar\nbaz\n' | grep ba | wc -l`,
			stdout:   "2\n",
			exitCode: 0,
		},
		// logical operators
		{
			cmdline:  `true && echo ok`,
			stdout:   "ok\n",
			exitCode: 0,
		},
		{
			cmdline:  `false || echo fallback`,
			stdout:   "fallback\n",
			exitCode: 0,
		},
		// variable expansion
		{
			cmdline:  `X=42; echo "value=$X"`,
			stdout:   "value=42\n",
			exitCode: 0,
		},
		// command substitution
		{
			cmdline:  `echo "count=$(printf 'a\nb\nc\n' | wc -l)"`,
			stdout:   "count=3\n",
			exitCode: 0,
		},
		// non-zero exit code is propagated
		{
			cmdline:  `exit 3`,
			stdout:   "",
			exitCode: 3,
		},
	}

	ctx := context.Background()
	for _, test := range tests {
		stdout, _, exitCode, _ := runShellCommandWithContext(ctx, test.cmdline)

		if stdout != test.stdout {
			t.Errorf("commandline '%s': expected stdout %q, got %q", test.cmdline, test.stdout, stdout)
		}
		if exitCode != test.exitCode {
			t.Errorf("commandline '%s': expected exit code %d, got %d", test.cmdline, test.exitCode, exitCode)
		}
	}
}

// test that `runShellCommandWithContext` honors context cancellation (timeout)
func TestRunShellCommandWithContextTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, _, exitCode, err := runShellCommandWithContext(ctx, `sleep 5`)

	if err == nil {
		t.Errorf("expected an error due to timeout, got nil (exit code %d)", exitCode)
	}
	if ctx.Err() != context.DeadlineExceeded {
		t.Errorf("expected context deadline exceeded, got %v", ctx.Err())
	}
}
