// mcp.go
//
// functions for MCP integration

package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/genai"

	"github.com/meinside/version-go"
)

const (
	mcpClientName = `gmn/mcp-client`
	mcpServerName = `gmn/mcp-server`

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

	return
}

// get a matched server name and tool from given mcp tools and function name
func mcpToolFrom(mcpConnsAndTools mcpConnectionsAndTools, fnName string) (serverKey string, serverType mcpServerType, mc *mcp.ClientSession, tool mcp.Tool, exists bool) {
	for serverKey, connsAndTools := range mcpConnsAndTools {
		for _, tool := range connsAndTools.tools {
			if tool != nil && tool.Name == fnName {
				return serverKey, connsAndTools.serverType, connsAndTools.connection, *tool, true
			}
		}
	}

	return "", "", nil, mcp.Tool{}, false
}

type mcpServerType string

const (
	mcpServerStreamable mcpServerType = "streamable"
	mcpServerStdio      mcpServerType = "stdio"
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
	url string,
) (connection *mcp.ClientSession, err error) {
	if connection, err = mcp.NewClient(
		&mcp.Implementation{
			Name:    mcpClientName,
			Version: version.Build(version.OS | version.Architecture),
		},
		&mcp.ClientOptions{},
	).Connect(
		ctx,
		mcp.NewStreamableClientTransport(
			url,
			&mcp.StreamableClientTransportOptions{
				HTTPClient: mcpHTTPClient(),
			},
		),
	); err == nil {
		return connection, nil
	}

	return nil, err
}

// run MCP server with given `cmdline`, connect to it, start, initialize, and return the client
func mcpRun(
	ctx context.Context,
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

	if connection, err = mcp.NewClient(
		&mcp.Implementation{
			Name:    mcpClientName,
			Version: version.Build(version.OS | version.Architecture),
		},
		&mcp.ClientOptions{},
	).Connect(
		ctx,
		mcp.NewCommandTransport(exec.Command(command, args...)),
	); err == nil {
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

// fetch function result from MCP server connection
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

// strip sensitive information from given server info
func stripServerInfo(serverType mcpServerType, info string) string {
	switch serverType {
	case mcpServerStreamable:
		return strings.Split(info, "?")[0]
	case mcpServerStdio:
		cmd, _, _ := parseCommandline(info)
		return cmd
	}
	return info
}
