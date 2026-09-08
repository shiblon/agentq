package mcp

import (
	"context"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// AllGoTools returns tools for working with Go code.
func AllGoTools() []server.ServerTool {
	return []server.ServerTool{
		goBuildTool(),
		goTestTool(),
		goFmtTool(),
		goVetTool(),
	}
}

func goBuildTool() server.ServerTool {
	def := mcplib.NewTool("go_build",
		mcplib.WithDescription("Build Go packages. Defaults to './...' (all packages in the module)."),
		mcplib.WithString("packages", mcplib.Description("Package pattern, e.g. './...' or './pkg/foo'")),
	)
	return server.ServerTool{Tool: def, Handler: withGrantCheck("go_build",
		func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
			pkgs := "./..."
			if p, ok := optionalStringArg(req, "packages"); ok {
				pkgs = p
			}
			return runInWorkdir(ctx, "go", "build", pkgs)
		})}
}

func goTestTool() server.ServerTool {
	def := mcplib.NewTool("go_test",
		mcplib.WithDescription("Run Go tests. Defaults to './...' with -count=1 to disable caching."),
		mcplib.WithString("packages", mcplib.Description("Package pattern, e.g. './...' or './pkg/foo'")),
		mcplib.WithString("run", mcplib.Description("Regex to filter test names, e.g. 'TestFoo'")),
	)
	return server.ServerTool{Tool: def, Handler: withGrantCheck("go_test",
		func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
			args := []string{"test", "-count=1"}
			if r, ok := optionalStringArg(req, "run"); ok {
				args = append(args, "-run", r)
			}
			pkgs := "./..."
			if p, ok := optionalStringArg(req, "packages"); ok {
				pkgs = p
			}
			args = append(args, pkgs)
			return runInWorkdir(ctx, "go", args...)
		})}
}

func goFmtTool() server.ServerTool {
	def := mcplib.NewTool("go_fmt",
		mcplib.WithDescription("Format Go source files. Defaults to all files in the module."),
		mcplib.WithString("packages", mcplib.Description("Package pattern")),
	)
	return server.ServerTool{Tool: def, Handler: withGrantCheck("go_fmt",
		func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
			pkgs := "./..."
			if p, ok := optionalStringArg(req, "packages"); ok {
				pkgs = p
			}
			return runInWorkdir(ctx, "gofmt", "-l", "-w", pkgs)
		})}
}

func goVetTool() server.ServerTool {
	def := mcplib.NewTool("go_vet",
		mcplib.WithDescription("Run go vet to check for common mistakes. Defaults to './...'."),
		mcplib.WithString("packages", mcplib.Description("Package pattern")),
	)
	return server.ServerTool{Tool: def, Handler: withGrantCheck("go_vet",
		func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
			pkgs := "./..."
			if p, ok := optionalStringArg(req, "packages"); ok {
				pkgs = p
			}
			return runInWorkdir(ctx, "go", "vet", pkgs)
		})}
}
