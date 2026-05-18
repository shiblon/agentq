package mcp

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// AllTools returns every tool registered in this package. The MCP server
// registers all tools; the per-session allowlist filter controls visibility.
func AllTools() []server.ServerTool {
	var tools []server.ServerTool
	tools = append(tools, AllFileTools()...)
	tools = append(tools, AllGitTools()...)
	tools = append(tools, AllGoTools()...)
	tools = append(tools, AllSearchTools()...)
	tools = append(tools, AllShellTools()...)
	return tools
}

// optionalStringArg extracts an optional string argument from a tool request.
// Returns ("", false) if the argument is absent or not a string.
func optionalStringArg(req mcplib.CallToolRequest, name string) (string, bool) {
	args, ok := req.Params.Arguments.(map[string]any)
	if !ok {
		return "", false
	}
	v, ok := args[name].(string)
	return v, ok && v != ""
}

// withAllowlistCheck wraps h so that it returns a tool error if the session's
// ToolAllowlist does not contain name. This is the per-call enforcement layer;
// tools/list filtering (via WithToolFilter) is the visibility layer.
func withAllowlistCheck(name string, h server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		c := claimsFromContext(ctx)
		if c == nil {
			return mcplib.NewToolResultError("no session claims in context"), nil
		}
		if !slices.Contains(c.ToolAllowlist, name) {
			return mcplib.NewToolResultError(fmt.Sprintf("tool %q not permitted for this session", name)), nil
		}
		return h(ctx, req)
	}
}

// chrootPath resolves requested (as the agent sees it, rooted at /) into a
// real filesystem path under workdir. Returns an error if workdir is empty
// (no filesystem access) or if the resolved path escapes workdir.
func chrootPath(workdir, requested string) (string, error) {
	if workdir == "" {
		return "", fmt.Errorf("no filesystem access configured for this session")
	}
	// Treat requested as absolute within the chroot root. filepath.Clean
	// clamps traversal at the root so ../../etc/passwd → /etc/passwd, which
	// filepath.Join then maps to workdir/etc/passwd.
	real := filepath.Join(workdir, filepath.Clean("/"+requested))
	root := filepath.Clean(workdir)
	if real != root && !strings.HasPrefix(real, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes working directory")
	}
	return real, nil
}
