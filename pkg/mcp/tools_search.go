package mcp

import (
	"context"
	"path/filepath"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// AllSearchTools returns tools for searching file content and names.
func AllSearchTools() []server.ServerTool {
	return []server.ServerTool{
		grepTool(),
		findFilesTool(),
	}
}

func grepTool() server.ServerTool {
	def := mcplib.NewTool("grep",
		mcplib.WithDescription("Search for a pattern in files using grep -rn. Paths are relative to the session root."),
		mcplib.WithString("pattern",
			mcplib.Required(),
			mcplib.Description("Search pattern (regular expression)"),
		),
		mcplib.WithString("path",
			mcplib.Description("Path to search within (default: session root)"),
		),
	)
	return server.ServerTool{Tool: def, Handler: withGrantCheck("grep",
		func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
			pattern, err := req.RequireString("pattern")
			if err != nil {
				return mcplib.NewToolResultError(err.Error()), nil
			}
			searchPath := "."
			if p, ok := optionalStringArg(req, "path"); ok {
				real, err := chrootPath(rootFromContext(ctx), p)
				if err != nil {
					return mcplib.NewToolResultError(err.Error()), nil
				}
				rel, err := filepath.Rel(rootFromContext(ctx), real)
				if err != nil {
					return mcplib.NewToolResultError(err.Error()), nil
				}
				searchPath = rel
			}
			return runInWorkdir(ctx, "grep", "-rn", "--", pattern, searchPath)
		})}
}

func findFilesTool() server.ServerTool {
	def := mcplib.NewTool("find_files",
		mcplib.WithDescription("Find files by name pattern using find. Paths are relative to the session root."),
		mcplib.WithString("pattern",
			mcplib.Required(),
			mcplib.Description("Filename pattern, e.g. '*.go' or 'main.go'"),
		),
		mcplib.WithString("path",
			mcplib.Description("Directory to search within (default: session root)"),
		),
	)
	return server.ServerTool{Tool: def, Handler: withGrantCheck("find_files",
		func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
			pattern, err := req.RequireString("pattern")
			if err != nil {
				return mcplib.NewToolResultError(err.Error()), nil
			}
			searchPath := "."
			if p, ok := optionalStringArg(req, "path"); ok {
				real, err := chrootPath(rootFromContext(ctx), p)
				if err != nil {
					return mcplib.NewToolResultError(err.Error()), nil
				}
				rel, err := filepath.Rel(rootFromContext(ctx), real)
				if err != nil {
					return mcplib.NewToolResultError(err.Error()), nil
				}
				searchPath = rel
			}
			return runInWorkdir(ctx, "find", searchPath, "-name", pattern)
		})}
}
