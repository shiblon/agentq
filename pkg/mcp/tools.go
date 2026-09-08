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

// toolLegs records what holding each tool costs a session. Every registered
// tool must appear here, and New fails if one does not, so a tool cannot be
// added without saying what it lights up.
//
// A tool at MaxLegs is grantable. A tool above it is not grantable by anyone
// and belongs in isolatedTools with a note on which leg isolation would drop.
var toolLegs = map[string]LegSet{
	// Reading returns content nobody on our side authored, out of a tree
	// only this session should see.
	"read_file":      Legs(Untrusted, Private),
	"list_directory": Legs(Untrusted, Private),
	"grep":           Legs(Untrusted, Private),
	"find_files":     Legs(Untrusted, Private),
	"git_status":     Legs(Untrusted, Private),
	"git_diff":       Legs(Untrusted, Private),
	"git_log":        Legs(Untrusted, Private),
	"go_vet":         Legs(Untrusted, Private),

	// Writing changes the tree without taking anything new in. git_push
	// reaches further than write_file, and scope rather than legs is what
	// bounds the difference.
	"write_file":       Legs(Private, Mutate),
	"create_directory": Legs(Private, Mutate),
	"go_fmt":           Legs(Private, Mutate),
	"git_add":          Legs(Private, Mutate),
	"git_commit":       Legs(Private, Mutate),
	"git_push":         Legs(Private, Mutate),

	// Dispatching hands work to another agent. It reads the session workdir
	// and creates state elsewhere, but ingests nothing.
	"dispatch_to_agent": Legs(Private, Mutate),

	// echo returns its own argument, so it costs nothing.
	"echo": Legs(),

	// All three, and therefore ungrantable. See isolatedTools.
	"go_build": Legs(Untrusted, Private, Mutate),
	"go_test":  Legs(Untrusted, Private, Mutate),
	"git_pull": Legs(Untrusted, Private, Mutate),
}

// isolatedTools names the tools that carry every leg as written, so no session
// may hold them. Each executes or imports content it did not author, inside a
// tree it can also change:
//
//	go_build, go_test  compile and run code out of the workdir
//	git_pull           imports foreign commits into the workdir
//
// They become grantable once the runner can drop a leg on their behalf, by
// running them with no egress or with nothing private mounted. Until that
// exists they are unregistered, and this is the list of what that work would
// unlock.
var isolatedTools = []string{"go_build", "go_test", "git_pull"}

// AllTools returns every grantable tool. Tools naming an entry in
// isolatedTools are left out, because no legs a session may hold would cover
// them.
func AllTools() []server.ServerTool {
	var tools []server.ServerTool
	tools = append(tools, AllFileTools()...)
	tools = append(tools, AllGitTools()...)
	tools = append(tools, AllGoTools()...)
	tools = append(tools, AllSearchTools()...)
	return grantable(tools)
}

// grantable drops any tool that cannot fit inside the per-session cap.
func grantable(tools []server.ServerTool) []server.ServerTool {
	out := make([]server.ServerTool, 0, len(tools))
	for _, t := range tools {
		if slices.Contains(isolatedTools, t.Tool.Name) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// validateToolLegs reports tools that are registered without a leg tag, or
// tagged over the cap while still being registered. Both are programming
// errors, caught at server construction rather than at first call.
func validateToolLegs(tools []server.ServerTool) error {
	var untagged, overCap []string
	for _, t := range tools {
		name := t.Tool.Name
		legs, ok := toolLegs[name]
		if !ok {
			untagged = append(untagged, name)
			continue
		}
		if err := legs.Valid(); err != nil {
			overCap = append(overCap, fmt.Sprintf("%s: %v", name, err))
		}
	}
	if len(untagged) > 0 {
		return fmt.Errorf("tools registered without a leg tag in toolLegs: %s", strings.Join(untagged, ", "))
	}
	if len(overCap) > 0 {
		return fmt.Errorf("tools registered above the leg cap (add them to isolatedTools): %s", strings.Join(overCap, "; "))
	}
	return nil
}

// LegsFor returns the legs a tool costs, and whether it is tagged at all.
func LegsFor(name string) (LegSet, bool) {
	legs, ok := toolLegs[name]
	return legs, ok
}

// GrantableWith returns the names of tools a session holding legs may call,
// in registration order. The supervisor is shown this rather than the whole
// catalogue, so it can only propose substeps that will actually be granted.
func GrantableWith(legs LegSet) []string {
	var names []string
	for _, t := range AllTools() {
		need, ok := toolLegs[t.Tool.Name]
		if ok && legs.Contains(need) {
			names = append(names, t.Tool.Name)
		}
	}
	return names
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

// withLegCheck wraps h so it returns a tool error unless the session's legs
// cover what name costs. This is the per-call enforcement layer; tools/list
// filtering (via WithToolFilter) is the visibility layer over the same rule.
func withLegCheck(name string, h server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		c := claimsFromContext(ctx)
		if c == nil {
			return mcplib.NewToolResultError("no session claims in context"), nil
		}
		need, ok := toolLegs[name]
		if !ok {
			return mcplib.NewToolResultError(fmt.Sprintf("tool %q has no leg tag", name)), nil
		}
		if !c.Legs.Contains(need) {
			return mcplib.NewToolResultError(fmt.Sprintf(
				"tool %q needs %s; this session permits %s", name, need, c.Legs)), nil
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
