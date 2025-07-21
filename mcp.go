// mcp.go
//
// functions for MCP integration

package main

import (
	"context"
	"net"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/genai"

	"github.com/meinside/version-go"
)

const (
	mcpClientName = `gmn/mcp`

	mcpDefaultTimeoutSeconds               = 120 // FIXME: ideally, should be 0 for keeping the connection
	mcpDefaultDialTimeoutSeconds           = 10
	mcpDefaultKeepAliveSeconds             = 60
	mcpDefaultIdleTimeoutSeconds           = 180
	mcpDefaultTLSHandshakeTimeoutSeconds   = 20
	mcpDefaultResponseHeaderTimeoutSeconds = 60
	mcpDefaultExpectContinueTimeoutSeconds = 15
)

// for reusing http client
var _mcpHTTPClient *http.Client

// helper function for generating a http client
func mcpHTTPClient() *http.Client {
	if _mcpHTTPClient == nil {
		_mcpHTTPClient = &http.Client{
			Timeout: defaultTimeoutSeconds * time.Second,
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout:   mcpDefaultDialTimeoutSeconds * time.Second,
					KeepAlive: mcpDefaultKeepAliveSeconds * time.Second,
				}).DialContext,
				IdleConnTimeout:       mcpDefaultIdleTimeoutSeconds * time.Second,
				TLSHandshakeTimeout:   mcpDefaultTLSHandshakeTimeoutSeconds * time.Second,
				ResponseHeaderTimeout: mcpDefaultResponseHeaderTimeoutSeconds * time.Second,
				ExpectContinueTimeout: mcpDefaultExpectContinueTimeoutSeconds * time.Second,
				DisableCompression:    true,
			},
		}
	}
	return _mcpHTTPClient
}

// extract keys from given tools
func keysFromTools(
	localTools []genai.Tool,
	mcpTools map[string][]*mcp.Tool,
) (localToolKeys, mcpToolKeys []string) {
	for _, tool := range localTools {
		for _, decl := range tool.FunctionDeclarations {
			localToolKeys = append(localToolKeys, decl.Name)
		}
	}
	for _, tools := range mcpTools {
		for _, tool := range tools {
			mcpToolKeys = append(mcpToolKeys, tool.Name)
		}
	}

	return
}

// get a matched server name and tool from given mcp tools and function name
func mcpToolFrom(mcpTools map[string][]*mcp.Tool, fnName string) (serverURL string, tool mcp.Tool, exists bool) {
	for serverURL, tools := range mcpTools {
		for _, tool := range tools {
			if tool != nil && tool.Name == fnName {
				return serverURL, *tool, true
			}
		}
	}

	return "", mcp.Tool{}, false
}

// connect to MCP server, start, initialize, and return the client
func mcpConnect(
	ctx context.Context,
	url string,
) (connection *mcp.ClientSession, err error) {
	streamable := mcp.NewStreamableClientTransport(
		url,
		&mcp.StreamableClientTransportOptions{
			HTTPClient: mcpHTTPClient(),
		},
	)

	client := mcp.NewClient(
		&mcp.Implementation{
			Name:    mcpClientName,
			Version: version.Build(version.OS | version.Architecture),
		},
		&mcp.ClientOptions{},
	)

	if connection, err = client.Connect(ctx, streamable); err == nil {
		return connection, nil
	}

	return nil, err
}

// fetch function declarations from MCP server connection
func fetchMCPTools(
	ctx context.Context,
	connection *mcp.ClientSession,
) (tools []*mcp.Tool, err error) {
	var listed *mcp.ListToolsResult
	if listed, err = connection.ListTools(ctx, &mcp.ListToolsParams{}); err == nil {
		return listed.Tools, nil
	}

	return
}

// fetch function result from MCP server
func fetchMCPToolCallResult(
	ctx context.Context,
	connection *mcp.ClientSession,
	fnName string, fnArgs map[string]any,
) (res *mcp.CallToolResult, err error) {
	if res, err = connection.CallTool(
		ctx,
		&mcp.CallToolParams{
			Name:      fnName,
			Arguments: fnArgs,
		},
	); err == nil {
		return res, nil
	}

	return
}
