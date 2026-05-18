package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// AllFileTools returns the file system tools: read, write, list, mkdir.
func AllFileTools() []server.ServerTool {
	return []server.ServerTool{
		readFileTool(),
		writeFileTool(),
		listDirectoryTool(),
		createDirectoryTool(),
	}
}

func readFileTool() server.ServerTool {
	def := mcplib.NewTool("read_file",
		mcplib.WithDescription("Read the contents of a file. Paths are relative to the session root (/)."),
		mcplib.WithString("path",
			mcplib.Required(),
			mcplib.Description("File path, e.g. /src/main.go or src/main.go"),
		),
	)
	return server.ServerTool{Tool: def, Handler: withAllowlistCheck("read_file", readFileHandler)}
}

func readFileHandler(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	c := claimsFromContext(ctx) // non-nil guaranteed by withAllowlistCheck
	path, err := req.RequireString("path")
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	real, err := chrootPath(c.Workdir, path)
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	content, err := os.ReadFile(real)
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("read %s: %v", path, err)), nil
	}
	return mcplib.NewToolResultText(string(content)), nil
}

func writeFileTool() server.ServerTool {
	def := mcplib.NewTool("write_file",
		mcplib.WithDescription("Write content to a file, creating parent directories as needed. Overwrites any existing content."),
		mcplib.WithString("path",
			mcplib.Required(),
			mcplib.Description("File path"),
		),
		mcplib.WithString("content",
			mcplib.Required(),
			mcplib.Description("Content to write"),
		),
	)
	return server.ServerTool{Tool: def, Handler: withAllowlistCheck("write_file", writeFileHandler)}
}

func writeFileHandler(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	c := claimsFromContext(ctx) // non-nil guaranteed by withAllowlistCheck
	path, err := req.RequireString("path")
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	content, err := req.RequireString("content")
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	real, err := chrootPath(c.Workdir, path)
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	if err := os.MkdirAll(filepath.Dir(real), 0755); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("create parent dirs for %s: %v", path, err)), nil
	}
	if err := os.WriteFile(real, []byte(content), 0644); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("write %s: %v", path, err)), nil
	}
	return mcplib.NewToolResultText(fmt.Sprintf("wrote %s", path)), nil
}

func listDirectoryTool() server.ServerTool {
	def := mcplib.NewTool("list_directory",
		mcplib.WithDescription("List the contents of a directory. Each entry is prefixed with [file] or [dir]."),
		mcplib.WithString("path",
			mcplib.Required(),
			mcplib.Description("Directory path"),
		),
	)
	return server.ServerTool{Tool: def, Handler: withAllowlistCheck("list_directory", listDirectoryHandler)}
}

func listDirectoryHandler(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	c := claimsFromContext(ctx) // non-nil guaranteed by withAllowlistCheck
	path, err := req.RequireString("path")
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	real, err := chrootPath(c.Workdir, path)
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	entries, err := os.ReadDir(real)
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("list %s: %v", path, err)), nil
	}
	var b strings.Builder
	for _, e := range entries {
		if e.IsDir() {
			fmt.Fprintf(&b, "[dir]  %s\n", e.Name())
		} else {
			fmt.Fprintf(&b, "[file] %s\n", e.Name())
		}
	}
	return mcplib.NewToolResultText(b.String()), nil
}

func createDirectoryTool() server.ServerTool {
	def := mcplib.NewTool("create_directory",
		mcplib.WithDescription("Create a directory and all necessary parent directories."),
		mcplib.WithString("path",
			mcplib.Required(),
			mcplib.Description("Directory path to create"),
		),
	)
	return server.ServerTool{Tool: def, Handler: withAllowlistCheck("create_directory", createDirectoryHandler)}
}

func createDirectoryHandler(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	c := claimsFromContext(ctx) // non-nil guaranteed by withAllowlistCheck
	path, err := req.RequireString("path")
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	real, err := chrootPath(c.Workdir, path)
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	if err := os.MkdirAll(real, 0755); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("mkdir %s: %v", path, err)), nil
	}
	return mcplib.NewToolResultText(fmt.Sprintf("created %s", path)), nil
}
