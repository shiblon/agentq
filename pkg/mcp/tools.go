package mcp

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// grantContextKey carries the grant that admitted the current call, so
// handlers read their constraints from the scope they were permitted under
// rather than from anything session-wide.
type grantContextKey struct{}

// grantFromContext returns the grant for the call in flight. Set by
// withGrantCheck, so handlers behind it can rely on it.
func grantFromContext(ctx context.Context) Grant {
	g, _ := ctx.Value(grantContextKey{}).(Grant)
	return g
}

// rootFromContext returns the admitting grant's root. Empty only for tools
// that take no path.
func rootFromContext(ctx context.Context) string {
	return grantFromContext(ctx).Scope.Root
}

// AllTools returns every tool the server offers. Registration is harmless on
// its own: a tool legsFor cannot price, such as go_test or git_pull, can never
// appear in a grant, so no session ever sees or reaches it.
func AllTools() []server.ServerTool {
	var tools []server.ServerTool
	tools = append(tools, AllFileTools()...)
	tools = append(tools, AllGitTools()...)
	tools = append(tools, AllGoTools()...)
	tools = append(tools, AllSearchTools()...)
	return tools
}

// withGrantCheck wraps h so the call runs only under a grant that permits it.
// It selects the grant by tool and, for path-taking tools, by which grant's
// root contains the requested path; that is how one session can read a
// trusted mount cheaply and its own tree expensively with the same verb.
func withGrantCheck(tool string, h server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		c := claimsFromContext(ctx)
		if c == nil {
			return mcplib.NewToolResultError("no session claims in context"), nil
		}
		requested, _ := optionalStringArg(req, "path")
		g, ok := c.Grants.Find(tool, requested)
		if !ok {
			return mcplib.NewToolResultError(fmt.Sprintf(
				"tool %q is not granted to this session (granted: %s)",
				tool, strings.Join(c.Grants.Tools(), ", "))), nil
		}
		if g.Scope.Root != "" && requested != "" {
			if _, err := chrootPath(g.Scope.Root, requested); err != nil {
				return mcplib.NewToolResultError(fmt.Sprintf("%s: %v", requested, err)), nil
			}
		}
		return h(context.WithValue(ctx, grantContextKey{}, g), req)
	}
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
