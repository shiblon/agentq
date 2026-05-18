// Package mcp provides a long-running MCP pool server for AgentQ.
// Each SSE connection is an independent session configured by a signed JWT
// passed as the ?token= query parameter. The JWT carries the tool allowlist
// and filesystem context for that session; no separate admin call is needed.
package mcp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"

	"github.com/google/uuid"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/lestrrat-go/jwx/v2/jwk"
)

// Config holds the configuration for the MCP pool server.
type Config struct {
	// Addr is the TCP listen address, e.g. ":8080".
	Addr string

	// PublicKeys is the JWKS used to verify session tokens.
	// Required unless InsecureNoAuth is true.
	PublicKeys jwk.Set

	// Issuer is the expected iss claim in session tokens.
	// Required unless InsecureNoAuth is true.
	Issuer string

	// InsecureSkipVerification disables JWT signature verification. The token
	// is still parsed and its Claims are used -- workdir and tool allowlist
	// remain dynamic per-session. Only the cryptographic proof of origin is
	// skipped. Never use in production.
	InsecureSkipVerification bool

	// DevTools enables development-only tools (e.g. echo). These tools must
	// never be available in production. Controlled independently of
	// InsecureSkipVerification -- both can be set independently.
	DevTools bool
}

type claimsContextKey struct{}

// claimsFromContext returns the Claims stored in ctx by the JWT middleware,
// or nil if the context carries no claims.
func claimsFromContext(ctx context.Context) *Claims {
	c, _ := ctx.Value(claimsContextKey{}).(*Claims)
	return c
}

// Server is a long-running MCP pool server. Each SSE connection is an
// independent session isolated by the Claims in its session JWT.
type Server struct {
	sse  *server.SSEServer
	http *http.Server

	mu     sync.RWMutex
	claims map[string]*Claims // mcp session ID → Claims
}

// New constructs a Server from cfg. Call Start to begin accepting connections.
func New(cfg Config) (*Server, error) {
	if !cfg.InsecureSkipVerification && cfg.PublicKeys == nil {
		return nil, fmt.Errorf("mcp: PublicKeys required (or set InsecureSkipVerification for dev)")
	}

	s := &Server{
		claims: make(map[string]*Claims),
	}

	hooks := &server.Hooks{}
	hooks.AddOnUnregisterSession(func(_ context.Context, session server.ClientSession) {
		s.mu.Lock()
		delete(s.claims, session.SessionID())
		s.mu.Unlock()
	})

	mcpSrv := server.NewMCPServer(
		"agentq-mcp", "1.0.0",
		server.WithToolCapabilities(true),
		server.WithToolFilter(s.allowlistFilter),
		server.WithHooks(hooks),
	)
	tools := AllTools()
	if cfg.DevTools {
		tools = append(tools, AllDevTools()...)
	}
	mcpSrv.AddTools(tools...)

	// sessionIDGen runs during handleSSE (at /sse connection time), after
	// jwtMiddleware has already validated the token and stored Claims in
	// r.Context(). We record Claims keyed by the new session ID so they
	// survive into later message requests.
	sessionIDGen := func(_ context.Context, r *http.Request) (string, error) {
		c, ok := r.Context().Value(claimsContextKey{}).(*Claims)
		if !ok || c == nil {
			return "", fmt.Errorf("mcp: session claims missing from context")
		}
		sid := uuid.NewString()
		s.mu.Lock()
		s.claims[sid] = c
		s.mu.Unlock()
		return sid, nil
	}

	// contextFunc runs during handleMessage (each /message POST).
	// It retrieves Claims from the store by ?sessionId= and adds them to the
	// mcp-go context so the tool filter and handlers can read them.
	contextFunc := server.WithSSEContextFunc(func(ctx context.Context, r *http.Request) context.Context {
		sid := r.URL.Query().Get("sessionId")
		s.mu.RLock()
		c := s.claims[sid]
		s.mu.RUnlock()
		if c == nil {
			return ctx
		}
		return context.WithValue(ctx, claimsContextKey{}, c)
	})

	sseSrv := server.NewSSEServer(mcpSrv,
		server.WithBaseURL("http://"+cfg.Addr),
		server.WithUseFullURLForMessageEndpoint(false),
		server.WithSessionIDGenerator(sessionIDGen),
		contextFunc,
	)
	s.sse = sseSrv

	s.http = &http.Server{
		Addr:    cfg.Addr,
		Handler: jwtMiddleware(cfg.PublicKeys, cfg.Issuer, cfg.InsecureSkipVerification, sseSrv),
	}

	return s, nil
}

// Handler returns the HTTP handler for the server. Useful in tests where
// the caller wants to wrap the handler with httptest.NewServer.
func (s *Server) Handler() http.Handler { return s.http.Handler }

// Start begins accepting connections. It blocks until ctx is cancelled or a
// fatal error occurs. Returns nil on clean shutdown.
func (s *Server) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		return fmt.Errorf("mcp: listen: %w", err)
	}
	errc := make(chan error, 1)
	go func() { errc <- s.http.Serve(ln) }()
	select {
	case <-ctx.Done():
		return s.Close(context.Background())
	case err := <-errc:
		if err == http.ErrServerClosed {
			return nil
		}
		return fmt.Errorf("mcp: serve: %w", err)
	}
}

// Close shuts the server down gracefully.
func (s *Server) Close(ctx context.Context) error {
	s.sse.CloseSessions()
	return s.http.Shutdown(ctx)
}

// jwtMiddleware validates ?token= on /sse requests and rejects with 401 on
// failure. When skipVerification is true the JWT signature is not checked --
// Claims are still parsed from the token payload and remain dynamic per-session.
// Requests to other paths (e.g. /message) pass through; their Claims were
// recorded at connection time in the Server's claims map.
func jwtMiddleware(pubKeys jwk.Set, issuer string, skipVerification bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sse" {
			raw := r.URL.Query().Get("token")
			if raw == "" {
				http.Error(w, "missing token", http.StatusUnauthorized)
				return
			}
			var (
				c   *Claims
				err error
			)
			if skipVerification {
				c, err = ParseInsecure(raw)
			} else {
				c, err = Parse(pubKeys, issuer, raw)
			}
			if err != nil {
				http.Error(w, "invalid token", http.StatusUnauthorized)
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), claimsContextKey{}, c))
		}
		next.ServeHTTP(w, r)
	})
}

// allowlistFilter is registered with WithToolFilter. It restricts the tools
// returned by tools/list to those named in the session's ToolAllowlist.
func (s *Server) allowlistFilter(ctx context.Context, tools []mcplib.Tool) []mcplib.Tool {
	c := claimsFromContext(ctx)
	if c == nil {
		return nil
	}
	allowed := make(map[string]bool, len(c.ToolAllowlist))
	for _, name := range c.ToolAllowlist {
		allowed[name] = true
	}
	filtered := make([]mcplib.Tool, 0, len(c.ToolAllowlist))
	for _, t := range tools {
		if allowed[t.Name] {
			filtered = append(filtered, t)
		}
	}
	return filtered
}
