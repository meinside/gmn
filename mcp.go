// mcp.go
//
// Things for MCP integration.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/genai"

	"github.com/meinside/version-go"
)

const (
	mcpClientName = `gmn/mcp-client`
	mcpServerName = `gmn/mcp-server`

	mcpToolNameSelf   = `gmn/mcp-self`
	mcpToolNameSkills = `gmn/mcp-skills`

	mcpDefaultDialTimeoutSeconds = 10
	// timeout for connecting to a MCP server and fetching its tools
	// (spawning a stdio server, `server/discover`, and `tools/list`)
	mcpDefaultConnectTimeoutSeconds        = 60
	mcpDefaultKeepAliveSeconds             = 60
	mcpDefaultIdleTimeoutSeconds           = 180
	mcpDefaultTLSHandshakeTimeoutSeconds   = 20
	mcpDefaultResponseHeaderTimeoutSeconds = 60
	mcpDefaultExpectContinueTimeoutSeconds = 15

	mcpDefaultMaxRetries = 3

	// TTL hint (in milliseconds) for `tools/list` results of local servers
	//
	// NOTE: tool lists of local servers are fixed for the lifetime of the
	// process, so clients may cache them for a while (SEP-2549).
	mcpToolsListTTLMs = 60 * 1000
)

// mcpCacheHintMiddleware generates a middleware which adds cache hints
// (`ttlMs`; SEP-2549, since protocol version 2026-07-28) to list results, so
// that clients can cache them instead of re-fetching on every request.
func mcpCacheHintMiddleware(ttlMs int) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(
			ctx context.Context,
			method string,
			req mcp.Request,
		) (mcp.Result, error) {
			res, err := next(ctx, method, req)
			if err != nil {
				return res, err
			}

			// NOTE: `cacheScope` is left as its default ("public"): list results
			// of these servers do not depend on the requesting user.
			switch result := res.(type) {
			case *mcp.ListToolsResult:
				if result.TTLMs <= 0 {
					result.TTLMs = ttlMs
				}
			case *mcp.ListPromptsResult:
				if result.TTLMs <= 0 {
					result.TTLMs = ttlMs
				}
			}

			return res, nil
		}
	}
}

// generate client options for MCP client sessions
//
// NOTE: `ElicitationHandler` is needed for servers which request additional
// input (through the MRTR pattern since protocol version 2026-07-28).
func mcpClientOptions(
	writer outputWriter,
	p params,
) *mcp.ClientOptions {
	return &mcp.ClientOptions{
		ElicitationHandler: mcpElicitationHandler(writer, p),

		// show progress of long-running requests in verbose mode
		//
		// NOTE: request-scoped notifications like `notifications/progress` flow on
		// the response stream of the request they relate to.
		ProgressNotificationHandler: func(
			_ context.Context,
			request *mcp.ProgressNotificationClientRequest,
		) {
			if request == nil || request.Params == nil {
				return
			}

			progress := fmt.Sprintf("%.1f", request.Params.Progress)
			if request.Params.Total > 0 {
				progress = fmt.Sprintf(
					"%.1f/%.1f",
					request.Params.Progress,
					request.Params.Total,
				)
			}

			writer.verbose(
				verboseMedium,
				p.Verbose,
				"progress from MCP server: %s %s",
				progress,
				request.Params.Message,
			)
		},
	}
}

// mcpConnectionDetails holds the details of an MCP server connection and its tools.
type mcpConnectionDetails struct {
	serverType mcpServerType
	connection *mcp.ClientSession
	tools      []*mcp.Tool
}

// fetchAndRegisterMCPTools connects to an MCP server and fetches its tools.
func fetchAndRegisterMCPTools(
	ctx context.Context,
	writer outputWriter,
	p params,
	serverType mcpServerType,
	serverIdentifier string,
) (*mcpConnectionDetails, error) {
	writer.verbose(
		verboseMedium,
		p.Verbose,
		"fetching tools from MCP server: %s",
		stripServerInfo(serverType, serverIdentifier),
	)

	var mc *mcp.ClientSession
	var err error
	// NOTE: the given context bounds the connect + `tools/list` phase only; MCP
	// sessions are not tied to the context which created them (they live until
	// `Close()`), so cancelling it later does not kill the session.
	switch serverType {
	case mcpServerStreamable:
		mc, err = mcpConnect(ctx, writer, p, serverIdentifier)
	case mcpServerStdio:
		mc, err = mcpRun(ctx, writer, p, serverIdentifier)
	default:
		return nil, fmt.Errorf("unsupported MCP server type: %v", serverType)
	}

	if err != nil {
		return nil, fmt.Errorf(
			"failed to connect to MCP server '%s': %w",
			stripServerInfo(serverType, serverIdentifier),
			err,
		)
	}

	// show the negotiated protocol version and the server's identity
	//
	// NOTE: since protocol version 2026-07-28, this is the result of
	// `server/discover` (or of the legacy `initialize` handshake, when the server
	// does not support it yet)
	if initialized := mc.InitializeResult(); initialized != nil {
		serverInfo := "unknown"
		if initialized.ServerInfo != nil {
			serverInfo = fmt.Sprintf(
				"%s/%s",
				initialized.ServerInfo.Name,
				initialized.ServerInfo.Version,
			)
		}

		writer.verbose(
			verboseMedium,
			p.Verbose,
			"connected to MCP server: %s (protocol version: %s, server: %s)",
			stripServerInfo(serverType, serverIdentifier),
			initialized.ProtocolVersion,
			serverInfo,
		)
	}

	fetchedTools, err := fetchMCPTools(ctx, mc)
	if err != nil {
		_ = mc.Close() // Ensure connection is closed on fetch error
		return nil, fmt.Errorf(
			"failed to fetch tools from MCP server '%s': %w",
			stripServerInfo(serverType, serverIdentifier),
			err,
		)
	}

	return &mcpConnectionDetails{
		serverType: serverType,
		connection: mc,
		tools:      fetchedTools,
	}, nil
}

// for reusing http client
var (
	_mcpHTTPClient     *http.Client
	_mcpHTTPClientOnce sync.Once
)

// helper function for generating a http client
//
// NOTE: no `Timeout` is set on purpose; since protocol version 2026-07-28,
// stream resumability (`Last-Event-ID`) and message redelivery were removed
// from the streamable HTTP transport, so a response stream which is cut by a
// client-side timeout loses the in-flight request for good. Per-request
// deadlines are managed with contexts instead.
func mcpHTTPClient() *http.Client {
	_mcpHTTPClientOnce.Do(func() {
		_mcpHTTPClient = &http.Client{
			Transport: mcpHTTPTransport(),
		}
	})
	return _mcpHTTPClient
}

// helper function for generating a http client which adds the given headers to
// all requests
func mcpHTTPClientWithHeader(header http.Header) *http.Client {
	if len(header) == 0 {
		return mcpHTTPClient()
	}

	return &http.Client{
		Transport: &mcpHeaderRoundTripper{
			base:   mcpHTTPTransport(),
			header: header,
		},
	}
}

// mcpHeaderRoundTripper adds predefined headers to outgoing requests.
type mcpHeaderRoundTripper struct {
	base   http.RoundTripper
	header http.Header
}

// RoundTrip implements `http.RoundTripper`.
func (rt *mcpHeaderRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	for key, values := range rt.header {
		cloned.Header.Del(key)
		for _, value := range values {
			cloned.Header.Add(key, value)
		}
	}
	return rt.base.RoundTrip(cloned)
}

// parse `--mcp-header` values (`URL|Header: value`) into headers per server URL
func mcpHeadersFrom(values []string) (headers map[string]http.Header, err error) {
	headers = map[string]http.Header{}

	for _, value := range values {
		url, rest, found := strings.Cut(value, "|")
		if !found {
			return nil, fmt.Errorf(
				"no '|' between the server url and the header in: '%s'",
				value,
			)
		}
		url = strings.TrimSpace(url)

		key, headerValue, found := strings.Cut(rest, ":")
		if !found {
			return nil, fmt.Errorf(
				"no ':' between the header name and its value in: '%s'",
				value,
			)
		}
		key = strings.TrimSpace(key)
		headerValue = strings.TrimSpace(headerValue)

		if len(url) == 0 || len(key) == 0 {
			return nil, fmt.Errorf(
				"empty server url or header name in: '%s'",
				value,
			)
		}

		if _, exists := headers[url]; !exists {
			headers[url] = http.Header{}
		}
		headers[url].Add(key, headerValue)
	}

	return headers, nil
}

// helper function for generating a http transport
func mcpHTTPTransport() *http.Transport {
	return &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   mcpDefaultDialTimeoutSeconds * time.Second,
			KeepAlive: mcpDefaultKeepAliveSeconds * time.Second,
		}).DialContext,
		IdleConnTimeout:       mcpDefaultIdleTimeoutSeconds * time.Second,
		TLSHandshakeTimeout:   mcpDefaultTLSHandshakeTimeoutSeconds * time.Second,
		ResponseHeaderTimeout: mcpDefaultResponseHeaderTimeoutSeconds * time.Second,
		ExpectContinueTimeout: mcpDefaultExpectContinueTimeoutSeconds * time.Second,
		DisableCompression:    true,
	}
}

// extract keys from given tools
func keysFromTools(
	localTools []genai.Tool,
	mcpConnsAndTools mcpConnectionsAndTools,
) (localToolKeys, mcpToolKeys []string) {
	for _, tool := range localTools {
		for _, decl := range tool.FunctionDeclarations {
			localToolKeys = append(localToolKeys, decl.Name)
		}
	}
	for _, connsAndTools := range mcpConnsAndTools {
		for _, tool := range connsAndTools.tools {
			mcpToolKeys = append(mcpToolKeys, tool.Name)
		}
	}

	return localToolKeys, mcpToolKeys
}

// get a matched server name and tool from given mcp tools and function name
func mcpToolFrom(
	mcpConnsAndTools mcpConnectionsAndTools,
	fnName string,
) (serverKey string, serverType mcpServerType, mc *mcp.ClientSession, tool mcp.Tool, exists bool) {
	for serverKey, connsAndTools := range mcpConnsAndTools {
		for _, tool := range connsAndTools.tools {
			if tool != nil && tool.Name == fnName {
				return serverKey, connsAndTools.serverType, connsAndTools.connection, *tool, true
			}
		}
	}

	return "", "", nil, mcp.Tool{}, false
}

// mcp server type
type mcpServerType string

const (
	mcpServerStreamable mcpServerType = "streamable"
	mcpServerStdio      mcpServerType = "stdio"
	mcpServerInMemory   mcpServerType = "in-memory"
)

// a map for keeping MCP connections and their tools
//
// * keys are identifiers of servers (server url or commandline string)
type mcpConnectionsAndTools map[string]struct {
	serverType mcpServerType
	connection *mcp.ClientSession
	tools      []*mcp.Tool
}

// connect to MCP server, start, initialize, and return the client
func mcpConnect(
	ctx context.Context,
	writer outputWriter,
	p params,
	url string,
) (connection *mcp.ClientSession, err error) {
	// headers for this server, if any
	var headers map[string]http.Header
	if headers, err = mcpHeadersFrom(p.MCPTools.StreamableHTTPHeaders); err != nil {
		return nil, fmt.Errorf("failed to parse MCP headers: %w", err)
	}

	if connection, err = mcp.NewClient(
		&mcp.Implementation{
			Name:    mcpClientName,
			Version: version.Build(version.OS | version.Architecture),
		},
		mcpClientOptions(writer, p),
	).Connect(
		ctx,
		&mcp.StreamableClientTransport{
			Endpoint:   url,
			HTTPClient: mcpHTTPClientWithHeader(headers[url]),
			MaxRetries: mcpDefaultMaxRetries,
		},
		&mcp.ClientSessionOptions{},
	); err == nil {
		return connection, nil
	}

	return nil, err
}

// run MCP server with given `cmdline`, connect to it, start, initialize, and return the client
func mcpRun(
	ctx context.Context,
	writer outputWriter,
	p params,
	cmdline string,
) (connection *mcp.ClientSession, err error) {
	command, args, err := parseCommandline(cmdline)
	if err != nil {
		return nil, fmt.Errorf(
			"failed to parse command line `%s` %w",
			stripServerInfo(mcpServerStdio, cmdline),
			err,
		)
	}

	command = expandPath(command)

	if _, err = os.Stat(command); err != nil {
		return nil, err
	}

	cmd := exec.Command(command, args...)

	// NOTE: the logging feature of MCP is deprecated as of protocol version
	// 2026-07-28 (SEP-2577); stdio servers are expected to log to stderr, so
	// relay it (in verbose mode) instead of dropping it.
	if verboseLevel(p.Verbose) >= verboseMedium {
		cmd.Stderr = &mcpServerLogWriter{
			writer:     writer,
			p:          p,
			serverInfo: stripServerInfo(mcpServerStdio, cmdline),
		}
	}

	if connection, err = mcp.NewClient(
		&mcp.Implementation{
			Name:    mcpClientName,
			Version: version.Build(version.OS | version.Architecture),
		},
		mcpClientOptions(writer, p),
	).Connect(
		ctx,
		&mcp.CommandTransport{
			Command: cmd,
		},
		&mcp.ClientSessionOptions{},
	); err == nil {
		return connection, nil
	}

	return nil, err
}

// mcpServerLogWriter relays stderr of a stdio MCP server to the verbose output.
type mcpServerLogWriter struct {
	writer     outputWriter
	p          params
	serverInfo string

	buffer []byte
}

// Write implements `io.Writer`.
func (w *mcpServerLogWriter) Write(bs []byte) (n int, err error) {
	w.buffer = append(w.buffer, bs...)

	for {
		index := bytes.IndexByte(w.buffer, '\n')
		if index < 0 {
			break
		}
		line := strings.TrimSpace(string(w.buffer[:index]))
		w.buffer = w.buffer[index+1:]

		if len(line) > 0 {
			w.writer.verbose(
				verboseMedium,
				w.p.Verbose,
				"[%s] %s",
				w.serverInfo,
				line,
			)
		}
	}

	return len(bs), nil
}

// connect to local (in-memory) mcp server, start, initialize, and return the client
func mcpRunInMemory(
	ctx context.Context,
	writer outputWriter,
	p params,
	server *mcp.Server,
) (connection *mcp.ClientSession, err error) {
	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	// run server,
	if _, err = server.Connect(
		ctx,
		serverTransport,
		&mcp.ServerSessionOptions{},
	); err != nil {
		return nil, fmt.Errorf("failed to connect to in-memory mcp server: %w", err)
	}

	// connect to server,
	if connection, err = mcp.NewClient(
		&mcp.Implementation{
			Name:    mcpClientName,
			Version: version.Build(version.OS | version.Architecture),
		},
		mcpClientOptions(writer, p),
	).Connect(
		ctx,
		clientTransport,
		&mcp.ClientSessionOptions{},
	); err == nil {
		return connection, nil
	}

	return nil, err
}

// fetch function declarations from MCP server connection
//
// NOTE: iterates over all pages; servers may return `tools/list` results in
// multiple pages (SDK's `Tools` also fills the session's tool cache, which is
// needed for things like `x-mcp-header` injection).
func fetchMCPTools(
	ctx context.Context,
	connection *mcp.ClientSession,
) (tools []*mcp.Tool, err error) {
	for tool, err := range connection.Tools(ctx, &mcp.ListToolsParams{}) {
		if err != nil {
			return nil, err
		}
		tools = append(tools, tool)
	}

	return tools, nil
}

// fetch function result from MCP server connection
func fetchMCPToolCallResult(
	ctx context.Context,
	connection *mcp.ClientSession,
	fnName string, fnArgs map[string]any,
) (res *mcp.CallToolResult, err error) {
	params := &mcp.CallToolParams{
		Name:      fnName,
		Arguments: fnArgs,
	}

	// NOTE: servers only send `notifications/progress` for requests which carry
	// a progress token
	params.SetProgressToken(fmt.Sprintf(
		"%s-%d",
		fnName,
		time.Now().UnixNano(),
	))

	if res, err = connection.CallTool(ctx, params); err == nil {
		return res, nil
	}

	return res, err
}

// mcpToolCallFailed checks if the given tool call result is a failure.
//
// NOTE: `IsError` marks a failed tool execution, and `NeedsInput` marks an
// interim `input_required` result (MRTR; since protocol version 2026-07-28)
// which could not be fulfilled. Neither is a successful result.
func mcpToolCallFailed(res *mcp.CallToolResult) bool {
	return res != nil && (res.IsError || res.NeedsInput())
}

// mcpToolCallResponse builds a function response for the given tool call output.
func mcpToolCallResponse(
	output []genai.Part,
	failed bool,
) map[string]any {
	if failed {
		return map[string]any{
			"error": output,
		}
	}
	return map[string]any{
		"output": output,
	}
}

// mcpErrorResult returns an error CallToolResult with a formatted message.
func mcpErrorResult(format string, args ...any) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{
				Text: fmt.Sprintf(format, args...),
			},
		},
		IsError: true,
	}, nil
}

// mcpTextResult returns a success CallToolResult with text content.
func mcpTextResult(text string) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: text},
		},
	}, nil
}

// mcpJSONResult returns a success CallToolResult with both text and structured JSON.
func mcpJSONResult(marshalled []byte) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(marshalled)},
		},
		StructuredContent: json.RawMessage(marshalled),
	}, nil
}

// strip sensitive information from given server info
func stripServerInfo(
	serverType mcpServerType,
	info string,
) string {
	switch serverType {
	case mcpServerStreamable:
		return strings.Split(info, "?")[0]
	case mcpServerStdio:
		cmd, _, _ := parseCommandline(info)
		return cmd
	}
	return info
}
