package main

import (
	"os"
	"slices"
	"testing"
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
				`testin' commandline parsing`,
				`--phrase`,
				`"should work" correctly`,
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
		if slices.Equal(append([]string{cmdline}, args...), test.parsed) {
			t.Errorf("expected '%s', got '%s'", test.parsed, merged)
		}
	}
}
