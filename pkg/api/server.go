// Package api provides the agentq HTTP API server.
// Routes follow the pattern /api/v1/<resource>.
package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"github.com/shiblon/agentq/pkg/store"
	"github.com/shiblon/entroq"
)

// Server holds shared dependencies for all API handlers.
type Server struct {
	eq         *entroq.EntroQ
	store      *store.Store
	configFile string // path to agents.yaml; reloaded per-request for mutations
	auth       Authorizer
	staticDir  string // if set, serves static files (web UI) from this directory
}

// Option configures a Server.
type Option func(*Server)

// WithAuthorizer sets the request authorizer. Defaults to AllowAll.
func WithAuthorizer(a Authorizer) Option {
	return func(s *Server) { s.auth = a }
}

// WithStaticDir serves static files from dir at the root path, with SPA
// fallback: any path that doesn't match a file falls back to index.html.
// Typically set to web/dist after running `npm run build` in the web/ directory.
func WithStaticDir(dir string) Option {
	return func(s *Server) { s.staticDir = dir }
}

// New creates a Server backed by the given entroq client.
func New(eq *entroq.EntroQ, configFile string, opts ...Option) *Server {
	s := &Server{
		eq:         eq,
		store:      store.New(eq),
		configFile: configFile,
		auth:       AllowAll{},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Handler returns an http.Handler with all routes registered and middleware applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Health
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)

	// Sessions
	mux.HandleFunc("GET /api/v1/sessions", s.handleSessionsList)
	mux.HandleFunc("POST /api/v1/sessions", s.handleSessionsSubmit)
	mux.HandleFunc("GET /api/v1/sessions/{id}", s.handleSessionsGet)
	mux.HandleFunc("GET /api/v1/sessions/{id}/chain", s.handleSessionsChain)
	mux.HandleFunc("GET /api/v1/sessions/{id}/result", s.handleSessionsResult)

	// Agents
	mux.HandleFunc("GET /api/v1/agents", s.handleAgentsList)
	mux.HandleFunc("POST /api/v1/agents", s.handleAgentsAdd)
	mux.HandleFunc("DELETE /api/v1/agents/{name}", s.handleAgentsRemove)

	// Queues and review
	mux.HandleFunc("GET /api/v1/queues", s.handleQueuesList)
	mux.HandleFunc("GET /api/v1/review", s.handleReviewList)

	// Static file serving with SPA fallback (only when configured).
	if s.staticDir != "" {
		fs := http.FileServer(http.Dir(s.staticDir))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			// If the requested path exists on disk, serve it directly.
			// Otherwise fall back to index.html so the SPA router takes over.
			candidate := filepath.Join(s.staticDir, filepath.Clean("/"+r.URL.Path))
			if _, err := os.Stat(candidate); err == nil {
				fs.ServeHTTP(w, r)
			} else {
				http.ServeFile(w, r, filepath.Join(s.staticDir, "index.html"))
			}
		})
	}

	// Apply middleware: logging -> CORS -> auth -> mux
	var h http.Handler = mux
	h = authMiddleware(s.auth)(h)
	h = corsMiddleware(h)
	h = loggingMiddleware(h)
	return h
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- helpers -----------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}
