package api

import (
	"context"
	"log"
	"net/http"
	"time"
)

// Authorizer decides whether a request is permitted.
//
// The default implementation is AllowAll. To enforce real access control,
// implement this interface using OPA's rego package:
//
//	type OPAAuthorizer struct { query rego.PreparedEvalQuery }
//	func (a *OPAAuthorizer) Allow(ctx, r) (bool, error) {
//	    input := map[string]any{
//	        "method": r.Method,
//	        "path":   strings.Split(r.URL.Path, "/"),
//	        "user":   claimsFromJWT(r),   // extract from Authorization header
//	    }
//	    rs, _ := a.query.Eval(ctx, rego.EvalInput(input))
//	    return rs.Allowed(), nil
//	}
//
// A matching deploy/policy.rego shows the default allow-all policy with
// commented examples for tightening by role or path.
type Authorizer interface {
	Allow(ctx context.Context, r *http.Request) (bool, error)
}

// AllowAll is the default Authorizer: every request is permitted.
// Replace with an OPAAuthorizer to enforce access policies.
type AllowAll struct{}

func (AllowAll) Allow(_ context.Context, _ *http.Request) (bool, error) { return true, nil }

// authMiddleware rejects requests that the Authorizer denies.
func authMiddleware(auth Authorizer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ok, err := auth.Allow(r.Context(), r)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "authorization check failed")
				return
			}
			if !ok {
				writeError(w, http.StatusForbidden, "forbidden")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// loggingMiddleware logs each request with method, path, status, and duration.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		log.Printf("api %s %s -> %d (%s)", r.Method, r.URL.Path, rw.status, time.Since(start))
	})
}

// corsMiddleware adds permissive CORS headers for local development.
// Tighten AllowedOrigins before exposing to the internet.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// responseWriter wraps http.ResponseWriter to capture the status code.
type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}
