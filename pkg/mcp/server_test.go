package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	mcplib "github.com/mark3labs/mcp-go/mcp"
)

// handlerCapture is an http.Handler that records whether it was called and
// exposes the Claims found in the request context.
type handlerCapture struct {
	called bool
	claims *Claims
}

func (h *handlerCapture) ServeHTTP(_ http.ResponseWriter, r *http.Request) {
	h.called = true
	h.claims = claimsFromContext(r.Context())
}

// -- jwtMiddleware tests ------------------------------------------------------

func TestJWTMiddleware_ValidToken_PassesThrough(t *testing.T) {
	priv, pubSet := testKeyPair(t)
	c := Claims{
		Issuer:        "agentq",
		SessionID:     "sess-1",
		Workdir:       "/work",
		ToolAllowlist: []string{"read_file"},
	}
	tok, err := Mint(priv, c)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	capture := &handlerCapture{}
	handler := (&Server{pubKeys: pubSet, issuer: "agentq"}).jwtMiddlewareHandler(false, capture)

	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	r.Header.Set(SessionConfigHeader, tok)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !capture.called {
		t.Error("next handler was not called")
	}
	if capture.claims == nil {
		t.Fatal("Claims not in request context")
	}
	if capture.claims.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want sess-1", capture.claims.SessionID)
	}
}

func TestJWTMiddleware_MissingToken_Returns401(t *testing.T) {
	_, pubSet := testKeyPair(t)
	capture := &handlerCapture{}
	handler := (&Server{pubKeys: pubSet, issuer: "agentq"}).jwtMiddlewareHandler(false, capture)

	r := httptest.NewRequest(http.MethodPost, "/mcp", nil) // no header
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
	if capture.called {
		t.Error("next handler should not be called on missing token")
	}
}

func TestJWTMiddleware_ExpiredToken_Returns401(t *testing.T) {
	priv, pubSet := testKeyPair(t)
	c := Claims{
		Issuer:        "agentq",
		SessionID:     "sess-1",
		Workdir:       "/work",
		ToolAllowlist: []string{},
		Expiry:        time.Now().Add(-time.Minute),
	}
	tok, err := Mint(priv, c)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	capture := &handlerCapture{}
	handler := (&Server{pubKeys: pubSet, issuer: "agentq"}).jwtMiddlewareHandler(false, capture)

	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	r.Header.Set(SessionConfigHeader, tok)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
	if capture.called {
		t.Error("next handler should not be called on expired token")
	}
}

func TestJWTMiddleware_WrongIssuer_Returns401(t *testing.T) {
	priv, pubSet := testKeyPair(t)
	c := Claims{
		Issuer:        "other-issuer",
		SessionID:     "sess-1",
		Workdir:       "/work",
		ToolAllowlist: []string{},
	}
	tok, err := Mint(priv, c)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	capture := &handlerCapture{}
	handler := (&Server{pubKeys: pubSet, issuer: "agentq"}).jwtMiddlewareHandler(false, capture)

	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	r.Header.Set(SessionConfigHeader, tok)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestJWTMiddleware_AllPaths_RequireToken(t *testing.T) {
	// Streamable HTTP: every path requires the session config header,
	// not just a specific /sse endpoint.
	_, pubSet := testKeyPair(t)
	capture := &handlerCapture{}
	handler := (&Server{pubKeys: pubSet, issuer: "agentq"}).jwtMiddlewareHandler(false, capture)

	for _, path := range []string{"/mcp", "/health", "/anything"} {
		r := httptest.NewRequest(http.MethodPost, path, nil) // no header
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("path %s: status = %d, want 401", path, w.Code)
		}
	}
}

// -- allowlistFilter tests ----------------------------------------------------

func toolNames(tools []mcplib.Tool) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	return names
}

func makeTools(names ...string) []mcplib.Tool {
	tools := make([]mcplib.Tool, len(names))
	for i, name := range names {
		tools[i] = mcplib.NewTool(name, mcplib.WithDescription("test tool"))
	}
	return tools
}

func TestAllowlistFilter_FiltersToAllowlist(t *testing.T) {
	s := &Server{}
	c := &Claims{ToolAllowlist: []string{"read_file", "write_file"}}
	ctx := context.WithValue(context.Background(), claimsContextKey{}, c)

	all := makeTools("read_file", "write_file", "delete_file", "list_directory")
	got := s.allowlistFilter(ctx, all)

	if len(got) != 2 {
		t.Fatalf("len = %d, want 2; tools = %v", len(got), toolNames(got))
	}
	for _, tool := range got {
		if tool.Name != "read_file" && tool.Name != "write_file" {
			t.Errorf("unexpected tool %q in filtered result", tool.Name)
		}
	}
}

func TestAllowlistFilter_EmptyAllowlist_ReturnsEmpty(t *testing.T) {
	s := &Server{}
	c := &Claims{ToolAllowlist: []string{}}
	ctx := context.WithValue(context.Background(), claimsContextKey{}, c)

	got := s.allowlistFilter(ctx, makeTools("read_file", "write_file"))
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestAllowlistFilter_NoClaims_ReturnsNil(t *testing.T) {
	s := &Server{}
	got := s.allowlistFilter(context.Background(), makeTools("read_file"))
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestAllowlistFilter_AllowlistSupersetOfTools(t *testing.T) {
	s := &Server{}
	c := &Claims{ToolAllowlist: []string{"read_file", "write_file", "nonexistent"}}
	ctx := context.WithValue(context.Background(), claimsContextKey{}, c)

	// Only read_file is actually registered; nonexistent is in allowlist but not in tools.
	got := s.allowlistFilter(ctx, makeTools("read_file"))
	if len(got) != 1 || got[0].Name != "read_file" {
		t.Errorf("got %v, want [read_file]", toolNames(got))
	}
}
