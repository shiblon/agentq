package mcp

import (
	"context"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// AllDevTools returns tools intended for development and testing only.
// These tools must never appear in a production allowlist.
func AllDevTools() []server.ServerTool {
	return []server.ServerTool{
		echoTool(),
	}
}

func echoTool() server.ServerTool {
	def := mcplib.NewTool("echo",
		mcplib.WithDescription("Returns the input message unchanged. For development and testing only."),
		mcplib.WithString("message",
			mcplib.Required(),
			mcplib.Description("Message to echo back"),
		),
	)
	return server.ServerTool{Tool: def, Handler: withLegCheck("echo",
		func(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
			msg, err := req.RequireString("message")
			if err != nil {
				return mcplib.NewToolResultError(err.Error()), nil
			}
			return mcplib.NewToolResultText(msg), nil
		})}
}
