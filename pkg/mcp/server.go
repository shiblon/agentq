// Package mcp provides a long-running MCP pool server for AgentQ.
// Each request carries a signed JWT in the X-AgentQ-Session-Config header.
// The JWT carries the tool allowlist and filesystem context for that session;
// no separate admin call or persistent connection is needed.
package mcp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/shiblon/entroq"
)

// SessionConfigHeader is the HTTP header carrying the MCP session JWT.
// It is named to make clear this is configuration, not authentication.
const SessionConfigHeader = "X-AgentQ-Session-Config"

// Config holds the configuration for the MCP pool server.
type Config struct {
	// Addr is the TCP listen address, e.g. ":8081".
	Addr string

	// PublicKeys is the JWKS used to verify session tokens.
	// Required unless InsecureSkipVerification is true.
	PublicKeys jwk.Set

	// Issuer is the expected iss claim in session tokens.
	// Required unless InsecureSkipVerification is true.
	Issuer string

	// InsecureSkipVerification disables JWT signature verification. The token
	// is still parsed and its Claims are used -- workdir and tool allowlist
	// remain dynamic per-request. Only the cryptographic proof of origin is
	// skipped. Never use in production.
	InsecureSkipVerification bool

	// DevTools enables development-only tools (e.g. echo). These tools must
	// never be available in production. Controlled independently of
	// InsecureSkipVerification -- both can be set independently.
	DevTools bool

	// EQ is the EntroQ client used by orchestration tools (dispatch_to_agent).
	// If nil, orchestration tools are not registered.
	EQ *entroq.EntroQ

	// SupervisorQueue is this MCP server's supervisor inbox. Set as reply_to
	// on tasks created by dispatch_to_agent so leaf agents know where to return
	// results. Only used when EQ is set.
	SupervisorQueue string

	// QueueNamespace is the prefix for agent queue names, e.g. "agentq".
	// dispatch_to_agent constructs queues as <namespace>/<agent>/inbox.
	// Defaults to "agentq" when EQ is set.
	QueueNamespace string
}

type claimsContextKey struct{}

// claimsFromContext returns the Claims stored in ctx, or nil if absent.
func claimsFromContext(ctx context.Context) *Claims {
	c, _ := ctx.Value(claimsContextKey{}).(*Claims)
	return c
}

// Server is a long-running MCP pool server using the Streamable HTTP transport.
// Session configuration is carried in every request via X-AgentQ-Session-Config;
// the server is stateless -- no session store is maintained.
type Server struct {
	streamable *server.StreamableHTTPServer
	http       *http.Server

	keysMu  sync.RWMutex
	pubKeys jwk.Set // guarded by keysMu; use getPublicKeys / ReloadPublicKeys
	issuer  string
}

// ReloadPublicKeys atomically replaces the public key set used for JWT
// verification. Safe to call from a signal handler goroutine while the server
// is handling requests. Intended for SIGHUP-triggered key rotation when using
// --jwks-file with Vault Agent or similar. No-op in --insecure-skip-verification mode.
func (s *Server) ReloadPublicKeys(set jwk.Set) {
	s.keysMu.Lock()
	s.pubKeys = set
	s.keysMu.Unlock()
}

func (s *Server) getPublicKeys() jwk.Set {
	s.keysMu.RLock()
	defer s.keysMu.RUnlock()
	return s.pubKeys
}

// New constructs a Server from cfg. Call Start to begin accepting connections.
func New(cfg Config) (*Server, error) {
	if !cfg.InsecureSkipVerification && cfg.PublicKeys == nil {
		return nil, fmt.Errorf("mcp: PublicKeys required (or set InsecureSkipVerification for dev)")
	}

	s := &Server{
		pubKeys: cfg.PublicKeys,
		issuer:  cfg.Issuer,
	}

	mcpSrv := server.NewMCPServer(
		"agentq-mcp", "1.0.0",
		server.WithToolCapabilities(true),
		server.WithToolFilter(s.allowlistFilter),
	)
	tools := AllTools()
	if cfg.DevTools {
		tools = append(tools, AllDevTools()...)
	}
	if cfg.EQ != nil {
		ns := cfg.QueueNamespace
		if ns == "" {
			ns = "agentq"
		}
		tools = append(tools, AllOrchestrationTools(cfg.EQ, cfg.SupervisorQueue, ns)...)
	}
	mcpSrv.AddTools(tools...)

	// HTTPContextFunc fires on every request. The outer jwtMiddlewareHandler
	// has already validated the token and stored Claims in r.Context(); here
	// we propagate them into mcp-go's context for the tool filter and handlers.
	contextFunc := server.WithHTTPContextFunc(func(ctx context.Context, r *http.Request) context.Context {
		c, ok := r.Context().Value(claimsContextKey{}).(*Claims)
		if !ok || c == nil {
			return ctx
		}
		return context.WithValue(ctx, claimsContextKey{}, c)
	})

	s.streamable = server.NewStreamableHTTPServer(mcpSrv,
		server.WithStateLess(true),
		contextFunc,
	)

	s.http = &http.Server{
		Addr:    cfg.Addr,
		Handler: s.jwtMiddlewareHandler(cfg.InsecureSkipVerification, s.streamable),
	}

	return s, nil
}

// Handler returns the HTTP handler. Useful in tests with httptest.NewServer.
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
	return s.streamable.Shutdown(ctx)
}

// jwtMiddlewareHandler validates the X-AgentQ-Session-Config header on every
// request. When skipVerification is true the JWT signature is not checked --
// Claims are still parsed and remain dynamic per-request. Reads public keys
// from s.getPublicKeys() so ReloadPublicKeys takes effect without a restart.
func (s *Server) jwtMiddlewareHandler(skipVerification bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get(SessionConfigHeader)
		if raw == "" {
			http.Error(w, "missing "+SessionConfigHeader, http.StatusUnauthorized)
			return
		}
		var (
			c   *Claims
			err error
		)
		if skipVerification {
			c, err = ParseInsecure(raw)
		} else {
			c, err = Parse(s.getPublicKeys(), s.issuer, raw)
		}
		if err != nil {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), claimsContextKey{}, c))
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
