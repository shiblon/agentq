package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
)

// sessionCtx returns a context with Claims set, suitable for tool handler tests.
func sessionCtx(workdir string, tools ...string) context.Context {
	c := &Claims{
		Issuer:        "agentq",
		SessionID:     "test-sess",
		Workdir:       workdir,
		ToolAllowlist: tools,
	}
	return context.WithValue(context.Background(), claimsContextKey{}, c)
}

// callTool invokes handler with a simple string-argument map.
func callTool(ctx context.Context, handler func(context.Context, mcplib.CallToolRequest) (*mcplib.CallToolResult, error), args map[string]any) (*mcplib.CallToolResult, error) {
	req := mcplib.CallToolRequest{}
	req.Params.Arguments = args
	return handler(ctx, req)
}

// resultText returns the text content of a tool result, or "" if none.
func resultText(r *mcplib.CallToolResult) string {
	if r == nil || len(r.Content) == 0 {
		return ""
	}
	if t, ok := r.Content[0].(mcplib.TextContent); ok {
		return t.Text
	}
	return ""
}

// isError reports whether the tool result is an error result.
func isError(r *mcplib.CallToolResult) bool {
	return r != nil && r.IsError
}

// -- chrootPath tests ---------------------------------------------------------

func TestChrootPath_Normal(t *testing.T) {
	got, err := chrootPath("/work", "src/main.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/work/src/main.go" {
		t.Errorf("got %q, want /work/src/main.go", got)
	}
}

func TestChrootPath_AbsoluteInput(t *testing.T) {
	got, err := chrootPath("/work", "/src/main.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/work/src/main.go" {
		t.Errorf("got %q, want /work/src/main.go", got)
	}
}

func TestChrootPath_TraversalRejected(t *testing.T) {
	cases := []string{
		"../../etc/passwd",
		"../outside",
		"/../../etc/passwd",
		"foo/../../../escape",
	}
	for _, c := range cases {
		_, err := chrootPath("/work", c)
		// After clamping traversal at root, these should all resolve safely
		// under /work. The function should either succeed (clamped) or fail.
		// What matters: the result must never be outside /work.
		if err == nil {
			got, _ := chrootPath("/work", c)
			if !strings.HasPrefix(got, "/work") {
				t.Errorf("path %q escaped to %q", c, got)
			}
		}
	}
}

func TestChrootPath_EmptyWorkdir(t *testing.T) {
	_, err := chrootPath("", "foo/bar")
	if err == nil {
		t.Error("expected error for empty workdir")
	}
}

func TestChrootPath_WorkdirItself(t *testing.T) {
	got, err := chrootPath("/work", ".")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/work" {
		t.Errorf("got %q, want /work", got)
	}
}

// -- withAllowlistCheck tests -------------------------------------------------

func TestAllowlistCheck_Permitted(t *testing.T) {
	ctx := sessionCtx("/work", "read_file")
	called := false
	wrapped := withAllowlistCheck("read_file", func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		called = true
		return mcplib.NewToolResultText("ok"), nil
	})
	r, err := wrapped(ctx, mcplib.CallToolRequest{})
	if err != nil || isError(r) || !called {
		t.Errorf("expected pass-through; err=%v isError=%v called=%v", err, isError(r), called)
	}
}

func TestAllowlistCheck_NotPermitted(t *testing.T) {
	ctx := sessionCtx("/work", "list_directory") // read_file not in list
	called := false
	wrapped := withAllowlistCheck("read_file", func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		called = true
		return mcplib.NewToolResultText("ok"), nil
	})
	r, err := wrapped(ctx, mcplib.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !isError(r) {
		t.Error("expected tool error for non-permitted tool")
	}
	if called {
		t.Error("handler should not be called for non-permitted tool")
	}
}

func TestAllowlistCheck_NoClaims(t *testing.T) {
	wrapped := withAllowlistCheck("read_file", func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		return mcplib.NewToolResultText("ok"), nil
	})
	r, err := wrapped(context.Background(), mcplib.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !isError(r) {
		t.Error("expected tool error when no claims in context")
	}
}

// -- file tool handler tests --------------------------------------------------

func TestReadFile_Success(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello world"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	ctx := sessionCtx(dir, "read_file")
	r, err := callTool(ctx, readFileHandler, map[string]any{"path": "/hello.txt"})
	if err != nil || isError(r) {
		t.Fatalf("unexpected error; err=%v result=%v", err, resultText(r))
	}
	if resultText(r) != "hello world" {
		t.Errorf("content = %q, want %q", resultText(r), "hello world")
	}
}

func TestReadFile_NotFound(t *testing.T) {
	ctx := sessionCtx(t.TempDir(), "read_file")
	r, err := callTool(ctx, readFileHandler, map[string]any{"path": "/missing.txt"})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !isError(r) {
		t.Error("expected tool error for missing file")
	}
}

func TestReadFile_EmptyWorkdir(t *testing.T) {
	ctx := sessionCtx("", "read_file")
	r, err := callTool(ctx, readFileHandler, map[string]any{"path": "/hello.txt"})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !isError(r) {
		t.Error("expected tool error for empty workdir")
	}
}

func TestWriteFile_Success(t *testing.T) {
	dir := t.TempDir()
	ctx := sessionCtx(dir, "write_file")
	r, err := callTool(ctx, writeFileHandler, map[string]any{
		"path":    "/subdir/new.txt",
		"content": "written content",
	})
	if err != nil || isError(r) {
		t.Fatalf("unexpected error; err=%v result=%v", err, resultText(r))
	}
	got, err := os.ReadFile(filepath.Join(dir, "subdir", "new.txt"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "written content" {
		t.Errorf("content = %q, want %q", got, "written content")
	}
}

func TestWriteFile_CreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	ctx := sessionCtx(dir, "write_file")
	_, err := callTool(ctx, writeFileHandler, map[string]any{
		"path":    "/a/b/c/deep.txt",
		"content": "deep",
	})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a", "b", "c", "deep.txt")); err != nil {
		t.Errorf("file not created: %v", err)
	}
}

func TestListDirectory_Success(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte(""), 0644)
	os.Mkdir(filepath.Join(dir, "pkg"), 0755)

	ctx := sessionCtx(dir, "list_directory")
	r, err := callTool(ctx, listDirectoryHandler, map[string]any{"path": "/"})
	if err != nil || isError(r) {
		t.Fatalf("unexpected error; err=%v result=%v", err, resultText(r))
	}
	out := resultText(r)
	if !strings.Contains(out, "[file] a.go") {
		t.Errorf("missing file entry in %q", out)
	}
	if !strings.Contains(out, "[dir]  pkg") {
		t.Errorf("missing dir entry in %q", out)
	}
}

func TestCreateDirectory_Success(t *testing.T) {
	dir := t.TempDir()
	ctx := sessionCtx(dir, "create_directory")
	r, err := callTool(ctx, createDirectoryHandler, map[string]any{"path": "/new/nested/dir"})
	if err != nil || isError(r) {
		t.Fatalf("unexpected error; err=%v result=%v", err, resultText(r))
	}
	if _, err := os.Stat(filepath.Join(dir, "new", "nested", "dir")); err != nil {
		t.Errorf("directory not created: %v", err)
	}
}

func TestCreateDirectory_Idempotent(t *testing.T) {
	dir := t.TempDir()
	ctx := sessionCtx(dir, "create_directory")
	for i := range 2 {
		r, err := callTool(ctx, createDirectoryHandler, map[string]any{"path": "/repeated"})
		if err != nil || isError(r) {
			t.Fatalf("call %d: unexpected error; err=%v result=%v", i+1, err, resultText(r))
		}
	}
}
