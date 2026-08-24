// mcp_test.go
//
// Things for testing `mcp.go`.

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/genai"
)

// test that `fetchMCPTools` fetches tools from all pages of `tools/list`
func TestFetchMCPToolsWithPagination(t *testing.T) {
	ctx := context.TODO()

	// a server which returns only one tool per page
	server := mcp.NewServer(
		&mcp.Implementation{Name: "test-server", Version: "0.0.1"},
		&mcp.ServerOptions{PageSize: 1},
	)
	names := []string{"tool_a", "tool_b", "tool_c"}
	for _, name := range names {
		server.AddTool(
			&mcp.Tool{
				Name:        name,
				Description: "test tool",
				InputSchema: &jsonschema.Schema{Type: "object"},
			},
			func(
				context.Context,
				*mcp.CallToolRequest,
			) (*mcp.CallToolResult, error) {
				return mcpTextResult("ok")
			},
		)
	}

	conn, err := mcpRunInMemory(ctx, newStdoutWriter(), params{}, server)
	if err != nil {
		t.Fatalf("failed to run in-memory MCP server: %s", err)
	}
	defer func() { _ = conn.Close() }()

	tools, err := fetchMCPTools(ctx, conn)
	if err != nil {
		t.Fatalf("failed to fetch tools: %s", err)
	}

	if len(tools) != len(names) {
		t.Fatalf("expected %d tools, got %d", len(names), len(tools))
	}
	for i, name := range names {
		if tools[i].Name != name {
			t.Errorf("expected tool[%d] name '%s', got '%s'", i, name, tools[i].Name)
		}
	}
}

// test that failed tool call results are reported as errors, not outputs
func TestMCPToolCallResponse(t *testing.T) {
	output := []genai.Part{{Text: "result"}}

	// success
	if !mcpToolCallFailed(&mcp.CallToolResult{}) {
		res := mcpToolCallResponse(output, false)
		if _, exists := res["output"]; !exists {
			t.Errorf("expected 'output' key for a successful result, got: %v", res)
		}
	} else {
		t.Errorf("expected a plain result not to be a failure")
	}

	// error
	errored := &mcp.CallToolResult{IsError: true}
	if mcpToolCallFailed(errored) {
		res := mcpToolCallResponse(output, true)
		if _, exists := res["error"]; !exists {
			t.Errorf("expected 'error' key for an errored result, got: %v", res)
		}
		if _, exists := res["output"]; exists {
			t.Errorf("expected no 'output' key for an errored result, got: %v", res)
		}
	} else {
		t.Errorf("expected `IsError` result to be a failure")
	}

	// nil
	if mcpToolCallFailed(nil) {
		t.Errorf("expected nil result not to be a failure")
	}
}

// test that tool calls carry a progress token, and progress notifications from
// the server are delivered to the client
func TestMCPToolCallProgressNotifications(t *testing.T) {
	ctx := context.TODO()

	server := mcp.NewServer(
		&mcp.Implementation{Name: "test-server", Version: "0.0.1"},
		&mcp.ServerOptions{},
	)
	var receivedToken any
	server.AddTool(
		&mcp.Tool{
			Name:        "progressing_tool",
			Description: "test tool which reports progress",
			InputSchema: &jsonschema.Schema{Type: "object"},
		},
		func(
			ctx context.Context,
			request *mcp.CallToolRequest,
		) (*mcp.CallToolResult, error) {
			receivedToken = request.Params.GetProgressToken()
			if receivedToken != nil {
				_ = request.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
					ProgressToken: receivedToken,
					Progress:      1,
					Total:         2,
					Message:       "halfway",
				})
			}
			return mcpTextResult("done")
		},
	)

	conn, err := mcpRunInMemory(ctx, newStdoutWriter(), params{}, server)
	if err != nil {
		t.Fatalf("failed to run in-memory MCP server: %s", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err = fetchMCPToolCallResult(
		ctx,
		conn,
		"progressing_tool",
		map[string]any{},
	); err != nil {
		t.Fatalf("failed to call tool: %s", err)
	}

	if receivedToken == nil {
		t.Errorf("expected a progress token in the tool call params, got nil")
	}
}

// an output writer which captures verbose messages
type capturingWriter struct {
	outputWriter

	mutex    sync.Mutex
	captured []string
}

func (w *capturingWriter) verbose(
	targetLevel verbosity,
	verbosityFromParams []bool,
	format string,
	a ...any,
) {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	w.captured = append(w.captured, fmt.Sprintf(format, a...))
}

// capturedMessages returns a copy of the captured messages.
func (w *capturingWriter) capturedMessages() []string {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	return slices.Clone(w.captured)
}

// test that stderr of stdio MCP servers is relayed to the verbose output
func TestMCPServerLogWriter(t *testing.T) {
	writer := &capturingWriter{outputWriter: newStdoutWriter()}
	logWriter := &mcpServerLogWriter{
		writer:     writer,
		p:          params{Verbose: []bool{true, true}},
		serverInfo: "/path/to/server",
	}

	// partial lines should be buffered until a new line arrives
	if _, err := logWriter.Write([]byte("first line\nsecond ")); err != nil {
		t.Fatalf("failed to write: %s", err)
	}
	captured := writer.capturedMessages()
	if len(captured) != 1 {
		t.Fatalf("expected 1 captured line, got %d: %v", len(captured), captured)
	}
	if captured[0] != "[/path/to/server] first line" {
		t.Errorf("unexpected captured line: %s", captured[0])
	}

	if _, err := logWriter.Write([]byte("line\n\n")); err != nil {
		t.Fatalf("failed to write: %s", err)
	}
	captured = writer.capturedMessages()
	if len(captured) != 2 {
		t.Fatalf("expected 2 captured lines, got %d: %v", len(captured), captured)
	}
	if captured[1] != "[/path/to/server] second line" {
		t.Errorf("unexpected captured line: %s", captured[1])
	}
}

// test `mcpHeadersFrom` with valid and invalid values
func TestMCPHeadersFrom(t *testing.T) {
	headers, err := mcpHeadersFrom([]string{
		"https://server1/mcp|Authorization: Bearer xxxx",
		"https://server1/mcp|X-Extra: 1",
		"https://server2/mcp|Authorization: Basic yyyy",
	})
	if err != nil {
		t.Fatalf("failed to parse headers: %s", err)
	}

	if got := headers["https://server1/mcp"].Get("Authorization"); got != "Bearer xxxx" {
		t.Errorf("expected 'Bearer xxxx', got '%s'", got)
	}
	if got := headers["https://server1/mcp"].Get("X-Extra"); got != "1" {
		t.Errorf("expected '1', got '%s'", got)
	}
	if got := headers["https://server2/mcp"].Get("Authorization"); got != "Basic yyyy" {
		t.Errorf("expected 'Basic yyyy', got '%s'", got)
	}
	if got := headers["https://server2/mcp"].Get("X-Extra"); got != "" {
		t.Errorf("expected no 'X-Extra' for server2, got '%s'", got)
	}

	// invalid values
	for _, invalid := range []string{
		"https://server/mcp",                // no '|'
		"https://server/mcp|Authorization",  // no ':'
		"|Authorization: Bearer xxxx",       // no url
		"https://server/mcp| : Bearer xxxx", // no header name
	} {
		if _, err := mcpHeadersFrom([]string{invalid}); err == nil {
			t.Errorf("expected failure for '%s'", invalid)
		}
	}
}

// test that headers given for a server are sent with its requests
func TestMCPConnectWithHeaders(t *testing.T) {
	ctx := context.TODO()

	server := mcp.NewServer(
		&mcp.Implementation{Name: "test-server", Version: "0.0.1"},
		&mcp.ServerOptions{},
	)
	server.AddTool(
		&mcp.Tool{
			Name:        "noop",
			Description: "test tool",
			InputSchema: &jsonschema.Schema{Type: "object"},
		},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcpTextResult("ok")
		},
	)

	received := make(chan string, 10)
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true},
	)
	httpServer := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			received <- r.Header.Get("Authorization")
			handler.ServeHTTP(w, r)
		},
	))
	defer httpServer.Close()

	p := params{}
	p.MCPTools.StreamableHTTPHeaders = []string{
		httpServer.URL + "|Authorization: Bearer testtoken",
	}

	conn, err := mcpConnect(ctx, newStdoutWriter(), p, httpServer.URL)
	if err != nil {
		t.Fatalf("failed to connect: %s", err)
	}
	defer func() { _ = conn.Close() }()

	select {
	case got := <-received:
		if got != "Bearer testtoken" {
			t.Errorf("expected 'Bearer testtoken', got '%s'", got)
		}
	default:
		t.Errorf("no request was received by the server")
	}
}

// test that a server which needs additional input gets it from the elicitation
// handler, and its tool call succeeds on the retried request
//
// NOTE: since protocol version 2026-07-28, this round trip goes through the
// Multi Round-Trip Requests (MRTR) pattern: the server's result is an interim
// `input_required` one, the SDK's middleware fulfills the input requests with
// the client's handlers, then retries the original call.
func TestMCPToolCallWithElicitation(t *testing.T) {
	ctx := context.TODO()

	server := mcp.NewServer(
		&mcp.Implementation{Name: "test-server", Version: "0.0.1"},
		&mcp.ServerOptions{},
	)
	server.AddTool(
		&mcp.Tool{
			Name:        "greet",
			Description: "test tool which needs the user's name",
			InputSchema: &jsonschema.Schema{Type: "object"},
		},
		func(
			_ context.Context,
			request *mcp.CallToolRequest,
		) (*mcp.CallToolResult, error) {
			// the retried call carries the responses for the requested input
			if response, exists := request.Params.InputResponses["name-of-user"]; exists {
				elicited, ok := response.(*mcp.ElicitResult)
				if !ok {
					return mcpErrorResult("unexpected input response: %T", response)
				}
				if elicited.Action != "accept" {
					return mcpErrorResult("elicitation was not accepted: %s", elicited.Action)
				}

				return mcpTextResult(fmt.Sprintf("hello, %v!", elicited.Content["name"]))
			}

			// ask for the user's name (interim `input_required` result)
			return &mcp.CallToolResult{
				InputRequests: mcp.InputRequestMap{
					"name-of-user": &mcp.ElicitParams{
						Mode:    "form",
						Message: "what is your name?",
						RequestedSchema: map[string]any{
							"type": "object",
							"properties": map[string]any{
								"name": map[string]any{"type": "string"},
							},
							"required": []any{"name"},
						},
					},
				},
			}, nil
		},
	)

	// the user types their name
	withInteractiveStdin(t, "meinside\n")

	conn, err := mcpRunInMemory(ctx, newStdoutWriter(), params{}, server)
	if err != nil {
		t.Fatalf("failed to run in-memory MCP server: %s", err)
	}
	defer func() { _ = conn.Close() }()

	// NOTE: make sure the MRTR path (not the legacy server-initiated request
	// path) is the one being exercised
	if version := conn.InitializeResult().ProtocolVersion; version < "2026-07-28" {
		t.Fatalf("expected protocol version >= 2026-07-28, got: %s", version)
	}

	res, err := fetchMCPToolCallResult(ctx, conn, "greet", map[string]any{})
	if err != nil {
		t.Fatalf("failed to call tool: %s", err)
	}
	if mcpToolCallFailed(res) {
		t.Fatalf("tool call failed: %s", prettify(res.Content))
	}

	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected text content, got: %T", res.Content[0])
	}
	if text.Text != "hello, meinside!" {
		t.Errorf("expected 'hello, meinside!', got: %s", text.Text)
	}
}

// test connecting to a real (spawned) stdio MCP server: its tools are fetched
// and callable, and its stderr is relayed to the verbose output
func TestFetchAndRegisterMCPToolsOverStdio(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the stdio server test in short mode")
	}
	goCommand, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("no 'go' command for building the test server: %s", err)
	}

	// build the test stdio server
	serverPath := filepath.Join(t.TempDir(), "test-stdio-server")
	build := exec.Command(
		goCommand,
		"build",
		"-o", serverPath,
		"./testdata/mcp-stdio-server",
	)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("failed to build the test server: %s (%s)", err, string(output))
	}

	// NOTE: stderr of stdio servers is relayed in verbose mode
	p := params{Verbose: []bool{true, true}}
	writer := &capturingWriter{outputWriter: newStdoutWriter()}

	ctx, cancel := context.WithTimeout(
		context.TODO(),
		mcpDefaultConnectTimeoutSeconds*time.Second,
	)
	defer cancel()

	connDetails, err := fetchAndRegisterMCPTools(
		ctx,
		writer,
		p,
		mcpServerStdio,
		serverPath,
	)
	if err != nil {
		t.Fatalf("failed to connect to the stdio MCP server: %s", err)
	}
	defer func() { _ = connDetails.connection.Close() }()

	if len(connDetails.tools) != 1 || connDetails.tools[0].Name != "echo" {
		t.Fatalf("unexpected tools from the stdio server: %s", prettify(connDetails.tools))
	}

	// call its tool
	res, err := fetchMCPToolCallResult(
		ctx,
		connDetails.connection,
		"echo",
		map[string]any{"text": "hello stdio"},
	)
	if err != nil {
		t.Fatalf("failed to call tool: %s", err)
	}
	if mcpToolCallFailed(res) {
		t.Fatalf("tool call failed: %s", prettify(res.Content))
	}
	if text, ok := res.Content[0].(*mcp.TextContent); !ok || text.Text != "hello stdio" {
		t.Errorf("unexpected tool call result: %s", prettify(res.Content))
	}

	// the same server, launched from a quoted path which contains a space
	// (`parseCommandline` unquotes it before `exec.Command`)
	spacedDir := filepath.Join(t.TempDir(), "some dir")
	if err := os.MkdirAll(spacedDir, 0o750); err != nil {
		t.Fatalf("failed to create a directory with a space: %s", err)
	}
	spacedPath := filepath.Join(spacedDir, "test-stdio-server")
	if err := copyFile(serverPath, spacedPath); err != nil {
		t.Fatalf("failed to copy the test server: %s", err)
	}

	spacedConnDetails, err := fetchAndRegisterMCPTools(
		ctx,
		writer,
		p,
		mcpServerStdio,
		`"`+spacedPath+`" --stdio`,
	)
	if err != nil {
		t.Fatalf("failed to connect to the stdio MCP server at a quoted path: %s", err)
	}
	defer func() { _ = spacedConnDetails.connection.Close() }()

	if len(spacedConnDetails.tools) != 1 {
		t.Errorf("unexpected tools from the stdio server: %s", prettify(spacedConnDetails.tools))
	}

	// the server's stderr should be relayed (asynchronously)
	relayed := func(substring string) bool {
		for range 50 {
			for _, message := range writer.capturedMessages() {
				if strings.Contains(message, substring) {
					return true
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		return false
	}
	if !relayed("test stdio server started") {
		t.Errorf(
			"expected the server's stderr to be relayed, got: %s",
			prettify(writer.capturedMessages()),
		)
	}
	if !relayed("echoing: hello stdio") {
		t.Errorf(
			"expected the server's stderr for the tool call to be relayed, got: %s",
			prettify(writer.capturedMessages()),
		)
	}
}

// test that `stripServerInfo` hides potentially sensitive parts of server
// identifiers (query strings of urls, arguments of command lines)
func TestStripServerInfo(t *testing.T) {
	type test struct {
		serverType mcpServerType
		info       string
		expected   string
	}

	for _, tc := range []test{
		{
			serverType: mcpServerStreamable,
			info:       "https://server/mcp?user_id=1234&api_key=SECRET",
			expected:   "https://server/mcp",
		},
		{
			serverType: mcpServerStreamable,
			info:       "https://server/mcp",
			expected:   "https://server/mcp",
		},
		{
			serverType: mcpServerStdio,
			info:       "/path/to/server --token SECRET --stdio",
			expected:   "/path/to/server",
		},
		{
			serverType: mcpServerStdio,
			info:       `"/path/to/some server" --token SECRET`,
			expected:   `/path/to/some server`,
		},
		{
			serverType: mcpServerInMemory,
			info:       mcpToolNameSelf,
			expected:   mcpToolNameSelf,
		},
	} {
		if stripped := stripServerInfo(tc.serverType, tc.info); stripped != tc.expected {
			t.Errorf(
				"expected '%s' for %s server '%s', got '%s'",
				tc.expected,
				tc.serverType,
				tc.info,
				stripped,
			)
		}
		if strings.Contains(stripServerInfo(tc.serverType, tc.info), "SECRET") {
			t.Errorf("expected no secret in the stripped info of '%s'", tc.info)
		}
	}
}

// copyFile copies a file, keeping it executable.
func copyFile(from, to string) error {
	data, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return os.WriteFile(to, data, 0o750)
}
