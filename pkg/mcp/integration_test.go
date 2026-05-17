package mcp

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcplib "github.com/mark3labs/mcp-go/mcp"
)

func TestIntegration_ToolsListFilteredByAllowlist(t *testing.T) {
	priv, pubSet := testKeyPair(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("world"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	s, err := New(Config{Addr: "127.0.0.1:0", PublicKeys: pubSet, Issuer: "agentq"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	// Mint a token permitting only read_file.
	tok, err := Mint(priv, Claims{
		Issuer:        "agentq",
		SessionID:     "int-test-1",
		Workdir:       dir,
		ToolAllowlist: []string{"read_file"},
	})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	ctx := context.Background()
	c, err := mcpclient.NewSSEMCPClient(ts.URL + "/sse?token=" + tok)
	if err != nil {
		t.Fatalf("NewSSEMCPClient: %v", err)
	}
	defer c.Close()

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

	// tools/list should return only read_file.
	toolsResult, err := c.ListTools(ctx, mcplib.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(toolsResult.Tools) != 1 || toolsResult.Tools[0].Name != "read_file" {
		names := make([]string, len(toolsResult.Tools))
		for i, tool := range toolsResult.Tools {
			names[i] = tool.Name
		}
		t.Errorf("tools = %v, want [read_file]", names)
	}
}

func TestIntegration_ReadFile_InAllowlist(t *testing.T) {
	priv, pubSet := testKeyPair(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("world"), 0644)

	s, err := New(Config{Addr: "127.0.0.1:0", PublicKeys: pubSet, Issuer: "agentq"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	tok, err := Mint(priv, Claims{
		Issuer:        "agentq",
		SessionID:     "int-test-2",
		Workdir:       dir,
		ToolAllowlist: []string{"read_file"},
	})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	ctx := context.Background()
	c, err := mcpclient.NewSSEMCPClient(ts.URL + "/sse?token=" + tok)
	if err != nil {
		t.Fatalf("NewSSEMCPClient: %v", err)
	}
	defer c.Close()

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

	result, err := c.CallTool(ctx, mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      "read_file",
			Arguments: map[string]any{"path": "/hello.txt"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool error: %v", resultText(result))
	}
	if got := resultText(result); got != "world" {
		t.Errorf("content = %q, want world", got)
	}
}

func TestIntegration_WriteFile_NotInAllowlist(t *testing.T) {
	priv, pubSet := testKeyPair(t)
	dir := t.TempDir()

	s, err := New(Config{Addr: "127.0.0.1:0", PublicKeys: pubSet, Issuer: "agentq"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	// Only read_file in allowlist -- write_file must be blocked.
	tok, err := Mint(priv, Claims{
		Issuer:        "agentq",
		SessionID:     "int-test-3",
		Workdir:       dir,
		ToolAllowlist: []string{"read_file"},
	})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	ctx := context.Background()
	c, err := mcpclient.NewSSEMCPClient(ts.URL + "/sse?token=" + tok)
	if err != nil {
		t.Fatalf("NewSSEMCPClient: %v", err)
	}
	defer c.Close()

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

	result, err := c.CallTool(ctx, mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      "write_file",
			Arguments: map[string]any{"path": "/out.txt", "content": "blocked"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !result.IsError {
		t.Error("expected tool error for write_file not in allowlist")
	}
}
