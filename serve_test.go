// serve_test.go
//
// Things for testing `serve.go`.

package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// test that the self MCP server does not advertise deprecated capabilities
func TestSelfServerCapabilities(t *testing.T) {
	writer := newStdoutWriter()
	server, tools := buildSelfServer(writer, config{}, params{})
	if len(tools) == 0 {
		t.Fatalf("expected the self server to have tools")
	}

	conn, err := mcpRunInMemory(context.TODO(), writer, params{}, server)
	if err != nil {
		t.Fatalf("failed to run in-memory MCP server (self): %s", err)
	}
	defer func() { _ = conn.Close() }()

	capabilities := conn.InitializeResult().Capabilities
	if capabilities.Logging != nil {
		t.Errorf("expected no deprecated 'logging' capability, got: %v", capabilities.Logging)
	}
	if capabilities.Tools == nil {
		t.Errorf("expected inferred 'tools' capability, got nil")
	}
}

// test that the self MCP server returns cache hints for `tools/list` (SEP-2549)
func TestSelfServerToolsListCacheHints(t *testing.T) {
	writer := newStdoutWriter()
	server, _ := buildSelfServer(writer, config{}, params{})

	conn, err := mcpRunInMemory(context.TODO(), writer, params{}, server)
	if err != nil {
		t.Fatalf("failed to run in-memory MCP server (self): %s", err)
	}
	defer func() { _ = conn.Close() }()

	listed, err := conn.ListTools(context.TODO(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("failed to list tools: %s", err)
	}

	if listed.TTLMs != mcpToolsListTTLMs {
		t.Errorf("expected ttlMs %d, got %d", mcpToolsListTTLMs, listed.TTLMs)
	}
	if listed.CacheScope != "public" {
		t.Errorf("expected cacheScope 'public', got '%s'", listed.CacheScope)
	}
}

// test that structured results of the local tools match their declared output
// schemas (SEP-2106)
func TestSelfServerOutputSchemas(t *testing.T) {
	ctx := context.TODO()
	writer := newStdoutWriter()
	server, tools := buildSelfServer(writer, config{}, params{})

	conn, err := mcpRunInMemory(ctx, writer, params{}, server)
	if err != nil {
		t.Fatalf("failed to run in-memory MCP server (self): %s", err)
	}
	defer func() { _ = conn.Close() }()

	// a temporary text file for file tools
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(tmpFile, []byte("hello world\n"), 0o600); err != nil {
		t.Fatalf("failed to write temporary file: %s", err)
	}

	// a local http server for `gmn_do_http`
	testHTTPServer := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("hello http"))
		},
	))
	defer testHTTPServer.Close()

	calls := map[string]map[string]any{
		"gmn_get_cwd":        {},
		"gmn_stat_file":      {"filepath": tmpFile},
		"gmn_get_mimetype":   {"filepath": tmpFile},
		"gmn_list_files":     {"dirpath": tmpDir},
		"gmn_read_text_file": {"filepath": tmpFile},
		"gmn_run_cmdline":    {"cmdline": "echo hello"},
		"gmn_do_http": {
			"method": "GET",
			"url":    testHTTPServer.URL,
		},
	}

	for name, args := range calls {
		// find the declared output schema
		var schema *jsonschema.Schema
		for _, tool := range tools {
			if tool.Name == name {
				schema, _ = tool.OutputSchema.(*jsonschema.Schema)
				break
			}
		}
		if schema == nil {
			t.Errorf("no output schema declared for tool '%s'", name)
			continue
		}

		res, err := conn.CallTool(ctx, &mcp.CallToolParams{
			Name:      name,
			Arguments: args,
		})
		if err != nil {
			t.Errorf("failed to call tool '%s': %s", name, err)
			continue
		}
		if res.IsError {
			t.Errorf("tool '%s' returned an error: %s", name, prettify(res.Content))
			continue
		}
		if res.StructuredContent == nil {
			t.Errorf("tool '%s' returned no structured content", name)
			continue
		}

		resolved, err := schema.Resolve(nil)
		if err != nil {
			t.Errorf("failed to resolve output schema of '%s': %s", name, err)
			continue
		}

		// NOTE: validate the wire representation of the structured content
		marshalled, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Errorf("failed to marshal structured content of '%s': %s", name, err)
			continue
		}
		var unmarshalled any
		if err := json.Unmarshal(marshalled, &unmarshalled); err != nil {
			t.Errorf("failed to unmarshal structured content of '%s': %s", name, err)
			continue
		}
		if err := resolved.Validate(unmarshalled); err != nil {
			t.Errorf(
				"structured content of '%s' does not match its output schema: %s (content: %s)",
				name,
				err,
				string(marshalled),
			)
		}
	}
}

// test that the self MCP server can be served over (stateless) streamable HTTP
func TestSelfServerOverStreamableHTTP(t *testing.T) {
	ctx := context.TODO()
	writer := newStdoutWriter()
	server, _ := buildSelfServer(writer, config{}, params{})

	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true},
	))
	defer httpServer.Close()

	conn, err := mcpConnect(ctx, writer, params{}, httpServer.URL)
	if err != nil {
		t.Fatalf("failed to connect to streamable HTTP MCP server: %s", err)
	}
	defer func() { _ = conn.Close() }()

	tools, err := fetchMCPTools(ctx, conn)
	if err != nil {
		t.Fatalf("failed to fetch tools: %s", err)
	}
	if len(tools) == 0 {
		t.Fatalf("expected tools from the streamable HTTP server")
	}

	res, err := conn.CallTool(ctx, &mcp.CallToolParams{
		Name:      "gmn_get_cwd",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("failed to call tool: %s", err)
	}
	if res.IsError {
		t.Fatalf("tool returned an error: %s", prettify(res.Content))
	}
}

// find a free local address for tests
func freeLocalAddr(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on a free port: %s", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("failed to close the listener: %s", err)
	}

	return addr
}

// test the lifecycle of the standalone streamable HTTP server: it serves tool
// calls, and shuts down when its context is done
func TestRunStreamableHTTPServer(t *testing.T) {
	writer := newStdoutWriter()
	addr := freeLocalAddr(t)

	ctx, cancel := context.WithCancel(context.TODO())
	defer cancel()

	served := make(chan error, 1)
	go func() {
		served <- runStreamableHTTPServer(ctx, writer, config{}, params{}, addr)
	}()

	// wait for the server to start serving
	url := "http://" + addr
	var conn *mcp.ClientSession
	var err error
	for range 30 {
		connectCtx, cancelConnect := context.WithTimeout(context.TODO(), time.Second)
		conn, err = mcpConnect(connectCtx, writer, params{}, url)
		cancelConnect()

		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("failed to connect to the served MCP server: %s", err)
	}

	// call a tool over HTTP
	res, err := conn.CallTool(context.TODO(), &mcp.CallToolParams{
		Name:      "gmn_get_cwd",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("failed to call tool: %s", err)
	}
	if res.IsError {
		t.Fatalf("tool returned an error: %s", prettify(res.Content))
	}
	_ = conn.Close()

	// shut it down
	cancel()

	select {
	case err = <-served:
		if err != nil {
			t.Fatalf("expected a clean shutdown, got: %s", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("server did not shut down")
	}

	// and the address should not be served anymore
	connectCtx, cancelConnect := context.WithTimeout(context.TODO(), 2*time.Second)
	defer cancelConnect()
	if conn, err := mcpConnect(connectCtx, writer, params{}, url); err == nil {
		_ = conn.Close()
		t.Errorf("expected the server to be shut down, but it is still serving")
	}
}
