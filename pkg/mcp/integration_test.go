package mcp

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcplib "github.com/mark3labs/mcp-go/mcp"
)

// testSession holds a connected, initialized MCP client and its working directory.
type testSession struct {
	client *mcpclient.Client
	dir    string
	ctx    context.Context
}

// callTool is a convenience wrapper that fails the test on Go-level errors.
func (s *testSession) callTool(t *testing.T, name string, args map[string]any) *mcplib.CallToolResult {
	t.Helper()
	result, err := s.client.CallTool(s.ctx, mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{Name: name, Arguments: args},
	})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	return result
}

// newTestSession starts a Server backed by a temp directory, mints a JWT with
// the given tool allowlist, and returns a connected, initialized client.
// All cleanup is registered with t.Cleanup.
func newTestSession(t *testing.T, allowlist []string) *testSession {
	t.Helper()
	priv, pubSet := testKeyPair(t)
	dir := t.TempDir()

	s, err := New(Config{Addr: "127.0.0.1:0", PublicKeys: pubSet, Issuer: "agentq"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	tok, err := Mint(priv, Claims{
		Issuer:        "agentq",
		SessionID:     t.Name(),
		Workdir:       dir,
		ToolAllowlist: allowlist,
	})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	ctx := context.Background()
	c, err := mcpclient.NewStreamableHttpClient(ts.URL+"/mcp",
		transport.WithHTTPHeaders(map[string]string{
			SessionConfigHeader: tok,
		}),
	)
	if err != nil {
		t.Fatalf("NewStreamableHttpClient: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := c.Initialize(ctx, mcplib.InitializeRequest{
		Params: mcplib.InitializeParams{
			ProtocolVersion: mcplib.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcplib.Implementation{Name: "test", Version: "0.1"},
		},
	}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	return &testSession{client: c, dir: dir, ctx: ctx}
}

// -- tools/list ---------------------------------------------------------------

func TestIntegration_ToolsListFilteredByAllowlist(t *testing.T) {
	sess := newTestSession(t, []string{"read_file"})

	result, err := sess.client.ListTools(sess.ctx, mcplib.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(result.Tools) != 1 || result.Tools[0].Name != "read_file" {
		names := make([]string, len(result.Tools))
		for i, tool := range result.Tools {
			names[i] = tool.Name
		}
		t.Errorf("tools = %v, want [read_file]", names)
	}
}

// -- read_file ----------------------------------------------------------------

func TestIntegration_ReadFile_Success(t *testing.T) {
	sess := newTestSession(t, []string{"read_file"})

	if err := os.WriteFile(filepath.Join(sess.dir, "hello.txt"), []byte("world"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	result := sess.callTool(t, "read_file", map[string]any{"path": "/hello.txt"})
	if result.IsError {
		t.Fatalf("tool error: %v", resultText(result))
	}
	if got := resultText(result); got != "world" {
		t.Errorf("content = %q, want %q", got, "world")
	}
}

func TestIntegration_ReadFile_NotInAllowlist(t *testing.T) {
	sess := newTestSession(t, []string{"list_directory"})

	result := sess.callTool(t, "read_file", map[string]any{"path": "/anything.txt"})
	if !result.IsError {
		t.Error("expected tool error for read_file not in allowlist")
	}
}

// -- write_file ---------------------------------------------------------------

func TestIntegration_WriteFile_Success(t *testing.T) {
	sess := newTestSession(t, []string{"write_file"})

	result := sess.callTool(t, "write_file", map[string]any{
		"path":    "/output/result.txt",
		"content": "written by agent",
	})
	if result.IsError {
		t.Fatalf("tool error: %v", resultText(result))
	}

	// Verify the file actually appeared on the filesystem.
	got, err := os.ReadFile(filepath.Join(sess.dir, "output", "result.txt"))
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if string(got) != "written by agent" {
		t.Errorf("content = %q, want %q", got, "written by agent")
	}
}

func TestIntegration_WriteFile_NotInAllowlist(t *testing.T) {
	sess := newTestSession(t, []string{"read_file"})

	result := sess.callTool(t, "write_file", map[string]any{"path": "/out.txt", "content": "blocked"})
	if !result.IsError {
		t.Error("expected tool error for write_file not in allowlist")
	}
	// Confirm nothing was written.
	if _, err := os.Stat(filepath.Join(sess.dir, "out.txt")); !os.IsNotExist(err) {
		t.Error("file should not have been created")
	}
}

// -- list_directory -----------------------------------------------------------

func TestIntegration_ListDirectory_Success(t *testing.T) {
	sess := newTestSession(t, []string{"list_directory"})

	if err := os.WriteFile(filepath.Join(sess.dir, "a.go"), []byte(""), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.Mkdir(filepath.Join(sess.dir, "pkg"), 0755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	result := sess.callTool(t, "list_directory", map[string]any{"path": "/"})
	if result.IsError {
		t.Fatalf("tool error: %v", resultText(result))
	}
	out := resultText(result)
	if !strings.Contains(out, "[file] a.go") {
		t.Errorf("missing file entry in:\n%s", out)
	}
	if !strings.Contains(out, "[dir]  pkg") {
		t.Errorf("missing dir entry in:\n%s", out)
	}
}

// -- create_directory ---------------------------------------------------------

func TestIntegration_CreateDirectory_Success(t *testing.T) {
	sess := newTestSession(t, []string{"create_directory"})

	result := sess.callTool(t, "create_directory", map[string]any{"path": "/new/nested/dir"})
	if result.IsError {
		t.Fatalf("tool error: %v", resultText(result))
	}

	if _, err := os.Stat(filepath.Join(sess.dir, "new", "nested", "dir")); err != nil {
		t.Errorf("directory not created on filesystem: %v", err)
	}
}
