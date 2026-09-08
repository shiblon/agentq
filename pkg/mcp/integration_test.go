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

// newTestSession starts a Server backed by a temp directory, mints a JWT
// granting legs, and returns a connected, initialized client.
// All cleanup is registered with t.Cleanup.
func newTestSession(t *testing.T, legs LegSet) *testSession {
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
		Issuer:    "agentq",
		SessionID: t.Name(),
		Workdir:   dir,
		Legs:      legs,
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

func TestIntegration_ToolsListFilteredByLegs(t *testing.T) {
	// Legs, not names, decide visibility: an ingesting session sees every
	// reading tool and no mutating one, without either list being written
	// down anywhere.
	sess := newTestSession(t, Legs(Untrusted, Private))

	result, err := sess.client.ListTools(sess.ctx, mcplib.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	got := map[string]bool{}
	for _, tool := range result.Tools {
		got[tool.Name] = true
	}
	for _, want := range []string{"read_file", "list_directory", "grep", "git_diff"} {
		if !got[want] {
			t.Errorf("%q missing from tools/list: %v", want, toolNamesOf(result.Tools))
		}
	}
	for _, unwanted := range []string{"write_file", "git_push", "create_directory"} {
		if got[unwanted] {
			t.Errorf("%q visible to a session without the mutate leg", unwanted)
		}
	}
	// Ungrantable tools are not registered at all, so no legs reveal them.
	for _, never := range []string{"go_test", "go_build", "git_pull", "run_command"} {
		if got[never] {
			t.Errorf("%q is registered but carries more legs than any session may hold", never)
		}
	}
}

func toolNamesOf(tools []mcplib.Tool) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	return names
}

// -- read_file ----------------------------------------------------------------

func TestIntegration_ReadFile_Success(t *testing.T) {
	sess := newTestSession(t, Legs(Untrusted, Private))

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

func TestIntegration_ReadFile_LegsNotCovered(t *testing.T) {
	// A mutating session holds private+mutate, so it cannot ingest content.
	sess := newTestSession(t, Legs(Private, Mutate))

	result := sess.callTool(t, "read_file", map[string]any{"path": "/anything.txt"})
	if !result.IsError {
		t.Error("expected tool error: read_file needs the untrusted leg")
	}
}

// -- write_file ---------------------------------------------------------------

func TestIntegration_WriteFile_Success(t *testing.T) {
	sess := newTestSession(t, Legs(Private, Mutate))

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
	sess := newTestSession(t, Legs(Untrusted, Private))

	result := sess.callTool(t, "write_file", map[string]any{"path": "/out.txt", "content": "blocked"})
	if !result.IsError {
		t.Error("expected tool error: write_file needs the mutate leg")
	}
	// Confirm nothing was written.
	if _, err := os.Stat(filepath.Join(sess.dir, "out.txt")); !os.IsNotExist(err) {
		t.Error("file should not have been created")
	}
}

// -- list_directory -----------------------------------------------------------

func TestIntegration_ListDirectory_Success(t *testing.T) {
	sess := newTestSession(t, Legs(Untrusted, Private))

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
	sess := newTestSession(t, Legs(Private, Mutate))

	result := sess.callTool(t, "create_directory", map[string]any{"path": "/new/nested/dir"})
	if result.IsError {
		t.Fatalf("tool error: %v", resultText(result))
	}

	if _, err := os.Stat(filepath.Join(sess.dir, "new", "nested", "dir")); err != nil {
		t.Errorf("directory not created on filesystem: %v", err)
	}
}
