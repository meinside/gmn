// elicitation.go
//
// Things for handling MCP elicitation requests (`elicitation/create`).
//
// NOTE: Since protocol version 2026-07-28, servers request additional input
// through the Multi Round-Trip Requests (MRTR) pattern; the SDK's MRTR
// middleware fulfills those input requests by calling the handlers configured
// here, then retries the original request. Without an elicitation handler, any
// server which needs input fails with `client does not support elicitation`.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/fatih/color"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// elicitation actions
const (
	elicitationActionAccept  = `accept`
	elicitationActionDecline = `decline`
	elicitationActionCancel  = `cancel`
)

// check if stdin is attached to a terminal (= interactive)
//
// NOTE: kept in a variable for being replaceable in tests.
var stdinIsInteractive = func() bool {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) != 0
}

// generate an elicitation handler for MCP clients
func mcpElicitationHandler(
	writer outputWriter,
	p params,
) func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
	return func(
		_ context.Context,
		request *mcp.ElicitRequest,
	) (*mcp.ElicitResult, error) {
		if request == nil || request.Params == nil {
			return &mcp.ElicitResult{Action: elicitationActionCancel}, nil
		}

		mode := request.Params.Mode
		if mode == "" { // NOTE: mode is inferred from other fields when unset
			if request.Params.URL != "" {
				mode = "url"
			} else {
				mode = "form"
			}
		}

		// non-interactive: decline explicitly instead of hanging or erroring
		if !stdinIsInteractive() {
			writer.warn(
				"Declining elicitation request from MCP server (stdin is not interactive): %s",
				request.Params.Message,
			)
			return &mcp.ElicitResult{Action: elicitationActionDecline}, nil
		}

		writer.errorColored(
			color.FgHiCyan,
			"\nMCP server requests your input: %s\n",
			request.Params.Message,
		)

		switch mode {
		case "url":
			writer.errorColored(
				color.FgHiBlue,
				"%s\n",
				request.Params.URL,
			)

			if confirm("Did you complete the requested interaction?") {
				return &mcp.ElicitResult{Action: elicitationActionAccept}, nil
			}
			return &mcp.ElicitResult{Action: elicitationActionDecline}, nil
		case "form":
			content, action, err := elicitForm(writer, request.Params.RequestedSchema)
			if err != nil {
				writer.warn("Failed to read elicitation input: %s", err)
				return &mcp.ElicitResult{Action: elicitationActionCancel}, nil
			}
			if action != elicitationActionAccept {
				return &mcp.ElicitResult{Action: action}, nil
			}

			writer.verbose(
				verboseMedium,
				p.Verbose,
				"elicited values: %s",
				prettify(content, true),
			)

			return &mcp.ElicitResult{
				Action:  elicitationActionAccept,
				Content: content,
			}, nil
		default:
			writer.warn("Unsupported elicitation mode: %q", mode)
			return &mcp.ElicitResult{Action: elicitationActionDecline}, nil
		}
	}
}

// read values for the given (flat) elicitation schema from stdin
func elicitForm(
	writer outputWriter,
	requestedSchema any,
) (content map[string]any, action string, err error) {
	var schema *jsonschema.Schema
	if schema, err = elicitationSchemaFrom(requestedSchema); err != nil {
		return nil, elicitationActionCancel, err
	}

	content = map[string]any{}
	if schema == nil || len(schema.Properties) == 0 {
		// nothing to fill in; just ask for a confirmation
		if confirm("Accept?") {
			return content, elicitationActionAccept, nil
		}
		return nil, elicitationActionDecline, nil
	}

	for _, name := range elicitationPropertyNames(schema) {
		property := schema.Properties[name]
		required := slices.Contains(schema.Required, name)

		for {
			// NOTE: prompts are printed to stderr, so that they don't mix with
			// the generated output on stdout
			writer.errorColored(
				color.FgHiWhite,
				"%s: ",
				elicitationPrompt(name, property, required),
			)

			var read string
			if read, err = readLineFromStdin(); err != nil && len(read) == 0 {
				return nil, elicitationActionCancel, err
			}
			read = strings.TrimSpace(read)

			if read == "" {
				if !required {
					break // skip optional property
				}
				writer.warn("'%s' is required.", name)
				continue
			}

			var value any
			if value, err = elicitationValueFrom(read, property); err != nil {
				writer.warn("Invalid value for '%s': %s", name, err)
				err = nil
				continue
			}
			content[name] = value
			break
		}
	}

	return content, elicitationActionAccept, nil
}

// convert given raw requested schema into a *jsonschema.Schema
func elicitationSchemaFrom(requestedSchema any) (schema *jsonschema.Schema, err error) {
	if requestedSchema == nil {
		return nil, nil
	}
	if converted, ok := requestedSchema.(*jsonschema.Schema); ok {
		return converted, nil
	}

	var marshalled []byte
	if marshalled, err = json.Marshal(requestedSchema); err != nil {
		return nil, fmt.Errorf("failed to marshal requested schema: %w", err)
	}
	schema = &jsonschema.Schema{}
	if err = json.Unmarshal(marshalled, schema); err != nil {
		return nil, fmt.Errorf("failed to unmarshal requested schema: %w", err)
	}
	return schema, nil
}

// list property names of given schema: required ones first, then the others
// (both in a deterministic order)
func elicitationPropertyNames(schema *jsonschema.Schema) (names []string) {
	requireds := []string{}
	others := []string{}
	for name := range schema.Properties {
		if slices.Contains(schema.Required, name) {
			requireds = append(requireds, name)
		} else {
			others = append(others, name)
		}
	}
	slices.Sort(requireds)
	slices.Sort(others)

	return append(requireds, others...)
}

// build a prompt string for the given property
func elicitationPrompt(
	name string,
	property *jsonschema.Schema,
	required bool,
) string {
	prompt := name

	if property != nil {
		if len(property.Enum) > 0 {
			values := make([]string, len(property.Enum))
			for i, e := range property.Enum {
				values[i] = fmt.Sprintf("%v", e)
			}
			prompt += fmt.Sprintf(" [%s]", strings.Join(values, "|"))
		} else if property.Type != "" {
			prompt += fmt.Sprintf(" (%s)", property.Type)
		}

		if len(property.Description) > 0 {
			prompt += fmt.Sprintf(": %s", property.Description)
		}
	}

	if !required {
		prompt += " (optional, empty to skip)"
	}

	return prompt
}

// convert given string into a value which fits the given property's type
func elicitationValueFrom(
	read string,
	property *jsonschema.Schema,
) (value any, err error) {
	typ := ""
	if property != nil {
		typ = property.Type
	}

	switch typ {
	case "number":
		if value, err = strconv.ParseFloat(read, 64); err != nil {
			return nil, fmt.Errorf("not a number: %s", read)
		}
	case "integer":
		var parsed int64
		if parsed, err = strconv.ParseInt(read, 10, 64); err != nil {
			return nil, fmt.Errorf("not an integer: %s", read)
		}
		value = parsed
	case "boolean":
		var parsed bool
		if parsed, err = strconv.ParseBool(read); err != nil {
			return nil, fmt.Errorf("not a boolean: %s", read)
		}
		value = parsed
	default: // string, or unknown
		value = read
	}

	// check enum values
	if property != nil && len(property.Enum) > 0 {
		if !slices.ContainsFunc(property.Enum, func(e any) bool {
			return fmt.Sprintf("%v", e) == fmt.Sprintf("%v", value)
		}) {
			return nil, fmt.Errorf("not one of the allowed values: %s", read)
		}
	}

	return value, nil
}
