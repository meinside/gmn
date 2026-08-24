// elicitation_test.go
//
// Things for testing `elicitation.go`.

package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// test `elicitationSchemaFrom` with a raw (map) schema
func TestElicitationSchemaFrom(t *testing.T) {
	raw := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "your name",
			},
			"age": map[string]any{
				"type": "integer",
			},
		},
		"required": []any{"name"},
	}

	schema, err := elicitationSchemaFrom(raw)
	if err != nil {
		t.Fatalf("failed to convert raw schema: %s", err)
	}
	if len(schema.Properties) != 2 {
		t.Fatalf("expected 2 properties, got %d", len(schema.Properties))
	}
	if schema.Properties["name"].Description != "your name" {
		t.Errorf(
			"expected description 'your name', got '%s'",
			schema.Properties["name"].Description,
		)
	}
	if !slices.Equal(schema.Required, []string{"name"}) {
		t.Errorf("expected required ['name'], got %v", schema.Required)
	}

	// nil schema should not fail
	if schema, err = elicitationSchemaFrom(nil); err != nil || schema != nil {
		t.Errorf("expected (nil, nil) for nil schema, got (%v, %v)", schema, err)
	}
}

// test `elicitationPropertyNames`: required first, then others, both sorted
func TestElicitationPropertyNames(t *testing.T) {
	schema := &jsonschema.Schema{
		Properties: map[string]*jsonschema.Schema{
			"zebra":    {Type: "string"},
			"apple":    {Type: "string"},
			"required": {Type: "string"},
			"beta":     {Type: "string"},
		},
		Required: []string{"required"},
	}

	names := elicitationPropertyNames(schema)
	expected := []string{"required", "apple", "beta", "zebra"}
	if !slices.Equal(names, expected) {
		t.Errorf("expected %v, got %v", expected, names)
	}
}

// test `elicitationValueFrom` with various types
func TestElicitationValueFrom(t *testing.T) {
	type test struct {
		read     string
		property *jsonschema.Schema
		expected any
		fails    bool
	}

	tests := []test{
		{read: "hello", property: &jsonschema.Schema{Type: "string"}, expected: "hello"},
		{read: "42", property: &jsonschema.Schema{Type: "integer"}, expected: int64(42)},
		{read: "42.5", property: &jsonschema.Schema{Type: "integer"}, fails: true},
		{read: "42.5", property: &jsonschema.Schema{Type: "number"}, expected: 42.5},
		{read: "true", property: &jsonschema.Schema{Type: "boolean"}, expected: true},
		{read: "yes", property: &jsonschema.Schema{Type: "boolean"}, fails: true},
		{read: "no-type", property: nil, expected: "no-type"},
		{
			read:     "and",
			property: &jsonschema.Schema{Type: "string", Enum: []any{"and", "or"}},
			expected: "and",
		},
		{
			read:     "xor",
			property: &jsonschema.Schema{Type: "string", Enum: []any{"and", "or"}},
			fails:    true,
		},
	}

	for _, tc := range tests {
		value, err := elicitationValueFrom(tc.read, tc.property)
		if tc.fails {
			if err == nil {
				t.Errorf("expected failure for '%s', got value: %v", tc.read, value)
			}
			continue
		}
		if err != nil {
			t.Errorf("failed to convert '%s': %s", tc.read, err)
			continue
		}
		if value != tc.expected {
			t.Errorf(
				"expected %v (%T) for '%s', got %v (%T)",
				tc.expected, tc.expected, tc.read, value, value,
			)
		}
	}
}

// test that elicitation requests are declined (not failed) when stdin is not
// interactive
func TestElicitationHandlerDeclinesWhenNotInteractive(t *testing.T) {
	// NOTE: stdin of a test run may or may not be a terminal, so make it
	// deterministic
	original := stdinIsInteractive
	stdinIsInteractive = func() bool { return false }
	t.Cleanup(func() { stdinIsInteractive = original })

	handler := mcpElicitationHandler(newStdoutWriter(), params{})

	res, err := handler(context.TODO(), &mcp.ElicitRequest{
		Params: &mcp.ElicitParams{
			Mode:    "form",
			Message: "need your name",
			RequestedSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string"},
				},
				"required": []any{"name"},
			},
		},
	})
	if err != nil {
		t.Fatalf("expected no error, got: %s", err)
	}
	if res.Action != elicitationActionDecline {
		t.Errorf("expected action '%s', got '%s'", elicitationActionDecline, res.Action)
	}
}

// replace stdin with the given input, and mark it as interactive, for the
// duration of the test
func withInteractiveStdin(t *testing.T, input string) {
	t.Helper()

	originalStdin := _stdin
	originalReader := _stdinReader
	originalInteractive := stdinIsInteractive

	_stdin = strings.NewReader(input)
	_stdinReader = nil
	stdinIsInteractive = func() bool { return true }

	t.Cleanup(func() {
		_stdin = originalStdin
		_stdinReader = originalReader
		stdinIsInteractive = originalInteractive
	})
}

// requested schema for tests
func testElicitationSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "your name",
			},
			"age": map[string]any{
				"type": "integer",
			},
			"mode": map[string]any{
				"type": "string",
				"enum": []any{"and", "or"},
			},
		},
		"required": []any{"name", "age"},
	}
}

// test the interactive form elicitation: required properties are asked again
// when empty, invalid values are rejected, and optional ones can be skipped
func TestElicitationHandlerForm(t *testing.T) {
	// NOTE: required properties are asked first, in a deterministic
	// (alphabetical) order: 'age', 'name', then the optional 'mode'
	//
	// 'age' (required): empty first, then not a number, then given
	// 'name' (required): given
	// 'mode' (optional): skipped
	withInteractiveStdin(t, "\nnot-a-number\n42\nmeinside\n\n")

	handler := mcpElicitationHandler(newStdoutWriter(), params{})

	res, err := handler(context.TODO(), &mcp.ElicitRequest{
		Params: &mcp.ElicitParams{
			Mode:            "form",
			Message:         "who are you?",
			RequestedSchema: testElicitationSchema(),
		},
	})
	if err != nil {
		t.Fatalf("expected no error, got: %s", err)
	}

	if res.Action != elicitationActionAccept {
		t.Fatalf("expected action '%s', got '%s'", elicitationActionAccept, res.Action)
	}
	if res.Content["name"] != "meinside" {
		t.Errorf("expected name 'meinside', got: %v", res.Content["name"])
	}
	if res.Content["age"] != int64(42) {
		t.Errorf("expected age 42, got: %v (%T)", res.Content["age"], res.Content["age"])
	}
	if _, exists := res.Content["mode"]; exists {
		t.Errorf("expected skipped optional 'mode', got: %v", res.Content["mode"])
	}
}

// test that the form elicitation is cancelled when the input ends (eg. on ^D)
func TestElicitationHandlerFormCancelled(t *testing.T) {
	withInteractiveStdin(t, "") // NOTE: immediate EOF

	handler := mcpElicitationHandler(newStdoutWriter(), params{})

	res, err := handler(context.TODO(), &mcp.ElicitRequest{
		Params: &mcp.ElicitParams{
			Mode:            "form",
			Message:         "who are you?",
			RequestedSchema: testElicitationSchema(),
		},
	})
	if err != nil {
		t.Fatalf("expected no error, got: %s", err)
	}
	if res.Action != elicitationActionCancel {
		t.Errorf("expected action '%s', got '%s'", elicitationActionCancel, res.Action)
	}
}

// test the URL mode elicitation: accepted / declined by confirmation
func TestElicitationHandlerURL(t *testing.T) {
	type test struct {
		input    string
		expected string
	}

	for _, tc := range []test{
		{input: "y\n", expected: elicitationActionAccept},
		{input: "n\n", expected: elicitationActionDecline},
		{input: "", expected: elicitationActionDecline}, // NOTE: EOF
	} {
		withInteractiveStdin(t, tc.input)

		handler := mcpElicitationHandler(newStdoutWriter(), params{})

		res, err := handler(context.TODO(), &mcp.ElicitRequest{
			Params: &mcp.ElicitParams{
				Mode:    "url",
				Message: "please log in",
				URL:     "https://server/login",
			},
		})
		if err != nil {
			t.Fatalf("expected no error, got: %s", err)
		}
		if res.Action != tc.expected {
			t.Errorf(
				"expected action '%s' for input %q, got '%s'",
				tc.expected,
				tc.input,
				res.Action,
			)
		}
	}
}
