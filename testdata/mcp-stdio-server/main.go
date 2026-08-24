// main.go
//
// A minimal MCP server over STDIO, for testing the stdio transport of `gmn`.
//
// NOTE: not built as a part of `gmn` (it lives in `testdata/`); the test builds
// it with `go build` and runs it as a child process.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	// NOTE: stdio servers are expected to log to stderr
	_, _ = fmt.Fprintln(os.Stderr, "test stdio server started")

	server := mcp.NewServer(
		&mcp.Implementation{
			Name:    "test-stdio-server",
			Version: "0.0.1",
		},
		&mcp.ServerOptions{},
	)

	server.AddTool(
		&mcp.Tool{
			Name:        "echo",
			Description: "Echoes the given 'text' back.",
			InputSchema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"text": {Type: "string"},
				},
				Required: []string{"text"},
			},
		},
		func(
			_ context.Context,
			request *mcp.CallToolRequest,
		) (*mcp.CallToolResult, error) {
			var args struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(request.Params.Arguments, &args); err != nil {
				return &mcp.CallToolResult{
					Content: []mcp.Content{
						&mcp.TextContent{Text: fmt.Sprintf("invalid arguments: %s", err)},
					},
					IsError: true,
				}, nil
			}

			_, _ = fmt.Fprintf(os.Stderr, "echoing: %s\n", args.Text)

			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: args.Text},
				},
			}, nil
		},
	)

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "server error: %s\n", err)
		os.Exit(1)
	}
}
