package mcp

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	mcplib "github.com/mark3labs/mcp-go/mcp"
)

// runInWorkdir runs cmd with args in the root of the grant that admitted this
// call, combining stdout and stderr into a single tool result.
//
// TODO: passing (cmd, args...) is convenient but not inherently secure.
// Individual tool handlers are responsible for validating and sanitizing
// their specific arguments before calling this helper. In particular,
// user-controlled strings must never be interpolated into shell commands.
// See tool-config-generalization in engram short memory for the longer-term plan.
func runInWorkdir(ctx context.Context, cmd string, args ...string) (*mcplib.CallToolResult, error) {
	root := rootFromContext(ctx)
	if root == "" {
		return mcplib.NewToolResultError("no filesystem access configured for this session"), nil
	}

	command := exec.CommandContext(ctx, cmd, args...)
	command.Dir = root

	var out bytes.Buffer
	command.Stdout = &out
	command.Stderr = &out // merge: agents benefit from seeing error output inline

	if err := command.Run(); err != nil {
		// Non-zero exit is a tool error, not a Go error -- the agent should see
		// the output (which typically contains the error message).
		msg := strings.TrimSpace(out.String())
		if msg == "" {
			msg = fmt.Sprintf("%s: %v", cmd, err)
		}
		return mcplib.NewToolResultError(msg), nil
	}

	return mcplib.NewToolResultText(strings.TrimSpace(out.String())), nil
}
