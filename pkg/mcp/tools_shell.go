package mcp

import (
	"bytes"
	"context"
	"os/exec"
	"strings"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// AllShellTools returns the shell execution tool.
// run_command is intentionally excluded from all default tool sets and
// should be explicitly added to an agent's allowlist only when needed.
func AllShellTools() []server.ServerTool {
	return []server.ServerTool{
		runCommandTool(),
	}
}

func runCommandTool() server.ServerTool {
	def := mcplib.NewTool("run_command",
		mcplib.WithDescription("Run a shell command in the session working directory. "+
			"Use with caution: this tool grants broad execution capabilities. "+
			"It should rarely appear in production allowlists."),
		mcplib.WithString("command",
			mcplib.Required(),
			mcplib.Description("Shell command to execute via sh -c"),
		),
	)
	return server.ServerTool{Tool: def, Handler: withAllowlistCheck("run_command", runCommandHandler)}
}

func runCommandHandler(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	c := claimsFromContext(ctx) // non-nil guaranteed by withAllowlistCheck
	if c.Workdir == "" {
		return mcplib.NewToolResultError("no filesystem access configured for this session"), nil
	}
	command, err := req.RequireString("command")
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}

	// TODO: this is the most dangerous tool in the set. The command string is
	// passed verbatim to sh -c. Future hardening should restrict what commands
	// are allowed via a blocklist/allowlist in Claims (see tool-config-generalization).
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = c.Workdir

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(out.String())
		if msg == "" {
			msg = err.Error()
		}
		return mcplib.NewToolResultError(msg), nil
	}
	return mcplib.NewToolResultText(strings.TrimSpace(out.String())), nil
}
