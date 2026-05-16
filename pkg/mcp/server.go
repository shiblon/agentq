// Package mcp provides a per-invocation MCP server for sandboxing tool access.
// Each task gets its own server instance exposing only the tools that invocation
// is permitted to use. The server runs on a random localhost port and shuts down
// when the task completes.
package mcp

import (
	"context"
	"fmt"
	"net"
	"net/http"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Tool bundles an MCP tool definition with its handler.
type Tool = server.ServerTool

// NewTool constructs a Tool from a definition and handler.
func NewTool(def mcp.Tool, h server.ToolHandlerFunc) Tool {
	return server.ServerTool{Tool: def, Handler: h}
}

// Server is a short-lived MCP server scoped to one task invocation.
type Server struct {
	sse  *server.SSEServer
	http *http.Server
	url  string
}

// New starts an MCP server on a random localhost port, registering the given
// tools. The caller must call Close when the task is complete.
func New(tools []Tool) (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("mcp: listen: %w", err)
	}

	port := ln.Addr().(*net.TCPAddr).Port
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	mcpSrv := server.NewMCPServer("agentq", "1.0.0", server.WithToolCapabilities(true))
	mcpSrv.AddTools(tools...)

	sse := server.NewSSEServer(mcpSrv, server.WithBaseURL(baseURL))
	httpSrv := &http.Server{Handler: sse}

	go func() {
		if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			// Serve only returns on shutdown or hard error; nothing useful to do here.
			_ = err
		}
	}()

	return &Server{sse: sse, http: httpSrv, url: baseURL}, nil
}

// URL returns the base URL that MCP clients should connect to.
// The SSE endpoint is at URL()+"/sse".
func (s *Server) URL() string { return s.url }

// Close shuts the server down gracefully, closing active sessions first.
func (s *Server) Close(ctx context.Context) error {
	s.sse.CloseSessions()
	return s.http.Shutdown(ctx)
}
