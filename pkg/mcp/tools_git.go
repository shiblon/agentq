package mcp

import (
	"context"
	"path/filepath"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// AllGitTools returns the git tools.
func AllGitTools() []server.ServerTool {
	return []server.ServerTool{
		gitStatusTool(),
		gitDiffTool(),
		gitAddTool(),
		gitCommitTool(),
		gitLogTool(),
		gitPushTool(),
		gitPullTool(),
	}
}

func gitStatusTool() server.ServerTool {
	def := mcplib.NewTool("git_status",
		mcplib.WithDescription("Show the working tree status."),
	)
	return server.ServerTool{Tool: def, Handler: withGrantCheck("git_status",
		func(ctx context.Context, _ mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
			return runInWorkdir(ctx, "git", "status")
		})}
}

func gitDiffTool() server.ServerTool {
	def := mcplib.NewTool("git_diff",
		mcplib.WithDescription("Show changes between commits or working tree. Pass --staged to see staged changes."),
		mcplib.WithString("args",
			mcplib.Description("Optional git diff arguments, e.g. '--staged' or a file path"),
		),
	)
	return server.ServerTool{Tool: def, Handler: withGrantCheck("git_diff",
		func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
			args := []string{"diff"}
			if extra, ok := optionalStringArg(req, "args"); ok {
				args = append(args, extra)
			}
			return runInWorkdir(ctx, "git", args...)
		})}
}

func gitAddTool() server.ServerTool {
	def := mcplib.NewTool("git_add",
		mcplib.WithDescription("Stage files for commit. Use '.' to stage all changes."),
		mcplib.WithString("path",
			mcplib.Required(),
			mcplib.Description("File path or '.' for all"),
		),
	)
	return server.ServerTool{Tool: def, Handler: withGrantCheck("git_add",
		func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
			path, err := req.RequireString("path")
			if err != nil {
				return mcplib.NewToolResultError(err.Error()), nil
			}
			if path != "." {
				real, err := chrootPath(rootFromContext(ctx), path)
				if err != nil {
					return mcplib.NewToolResultError(err.Error()), nil
				}
				rel, err := filepath.Rel(rootFromContext(ctx), real)
				if err != nil {
					return mcplib.NewToolResultError(err.Error()), nil
				}
				path = rel
			}
			return runInWorkdir(ctx, "git", "add", path)
		})}
}

func gitCommitTool() server.ServerTool {
	def := mcplib.NewTool("git_commit",
		mcplib.WithDescription("Record staged changes with a commit message."),
		mcplib.WithString("message",
			mcplib.Required(),
			mcplib.Description("Commit message"),
		),
	)
	return server.ServerTool{Tool: def, Handler: withGrantCheck("git_commit",
		func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
			msg, err := req.RequireString("message")
			if err != nil {
				return mcplib.NewToolResultError(err.Error()), nil
			}
			return runInWorkdir(ctx, "git", "commit", "-m", msg)
		})}
}

func gitLogTool() server.ServerTool {
	def := mcplib.NewTool("git_log",
		mcplib.WithDescription("Show recent commit history (last 20 commits, one line each)."),
	)
	return server.ServerTool{Tool: def, Handler: withGrantCheck("git_log",
		func(ctx context.Context, _ mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
			return runInWorkdir(ctx, "git", "log", "--oneline", "-20")
		})}
}

func gitPushTool() server.ServerTool {
	def := mcplib.NewTool("git_push",
		mcplib.WithDescription("Push commits to a remote branch. Only branches inside the grant scope are permitted."),
		mcplib.WithString("remote", mcplib.Required(), mcplib.Description("Remote name, e.g. 'origin'")),
		mcplib.WithString("branch", mcplib.Required(), mcplib.Description("Branch to push to")),
	)
	return server.ServerTool{Tool: def, Handler: withGrantCheck("git_push",
		func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
			remote, err := req.RequireString("remote")
			if err != nil {
				return mcplib.NewToolResultError(err.Error()), nil
			}
			branch, err := req.RequireString("branch")
			if err != nil {
				return mcplib.NewToolResultError(err.Error()), nil
			}
			if !branchAllowed(grantFromContext(ctx).Scope.Branches, branch) {
				return mcplib.NewToolResultError("push to branch " + branch + " is not permitted by this session"), nil
			}
			return runInWorkdir(ctx, "git", "push", remote, branch)
		})}
}

func gitPullTool() server.ServerTool {
	def := mcplib.NewTool("git_pull",
		mcplib.WithDescription("Pull commits from a remote branch. Only branches inside the grant scope are permitted."),
		mcplib.WithString("remote", mcplib.Required(), mcplib.Description("Remote name, e.g. 'origin'")),
		mcplib.WithString("branch", mcplib.Required(), mcplib.Description("Branch to pull from")),
	)
	return server.ServerTool{Tool: def, Handler: withGrantCheck("git_pull",
		func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
			remote, err := req.RequireString("remote")
			if err != nil {
				return mcplib.NewToolResultError(err.Error()), nil
			}
			branch, err := req.RequireString("branch")
			if err != nil {
				return mcplib.NewToolResultError(err.Error()), nil
			}
			if !branchAllowed(grantFromContext(ctx).Scope.Branches, branch) {
				return mcplib.NewToolResultError("pull from branch " + branch + " is not permitted by this session"), nil
			}
			return runInWorkdir(ctx, "git", "pull", remote, branch)
		})}
}

// branchAllowed reports whether branch matches any pattern in allowed.
// "*" matches everything. Empty allowed means nothing is permitted.
func branchAllowed(allowed []string, branch string) bool {
	for _, pattern := range allowed {
		if pattern == "*" {
			return true
		}
		if matched, _ := filepath.Match(pattern, branch); matched {
			return true
		}
	}
	return false
}
