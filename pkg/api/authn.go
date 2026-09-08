package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

// Claims holds the validated identity extracted from a JWT.
type Claims struct {
	// Subject is the principal identifier (sub claim).
	Subject string

	// DelegatedBy is the human sub from the act claim, present only when
	// this token was issued via token exchange on behalf of a human.
	// An agent token always has this set; a human token never does.
	DelegatedBy string

	// IsAgent is true when DelegatedBy is non-empty, i.e. the act claim
	// is present in the token.
	IsAgent bool

	// Raw holds the full parsed token for OPA or other consumers.
	Raw jwt.Token
}

type claimsKey struct{}

// ClaimsFromContext returns the Claims stored in ctx, or nil if not present.
func ClaimsFromContext(ctx context.Context) *Claims {
	c, _ := ctx.Value(claimsKey{}).(*Claims)
	return c
}

// JWKSValidator validates JWTs against a JWKS endpoint, with automatic
// key refresh.
type JWKSValidator struct {
	cache   *jwk.Cache
	jwksURL string
	issuer  string

	mu      sync.RWMutex
	started bool
}

// NewJWKSValidator creates a validator that fetches keys from jwksURL and
// checks the iss claim against issuer.
func NewJWKSValidator(jwksURL, issuer string) *JWKSValidator {
	return &JWKSValidator{
		jwksURL: jwksURL,
		issuer:  issuer,
	}
}

// start lazily initializes the JWKS cache on first use.
func (v *JWKSValidator) start(ctx context.Context) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.started {
		return nil
	}
	cache := jwk.NewCache(ctx)
	if err := cache.Register(v.jwksURL, jwk.WithMinRefreshInterval(15*time.Minute)); err != nil {
		return fmt.Errorf("register jwks: %w", err)
	}
	// Warm the cache immediately so the first request doesn't block.
	if _, err := cache.Refresh(ctx, v.jwksURL); err != nil {
		return fmt.Errorf("warm jwks cache: %w", err)
	}
	v.cache = cache
	v.started = true
	return nil
}

// Validate parses and validates the JWT in the Authorization header, returning
// populated Claims on success.
func (v *JWKSValidator) Validate(ctx context.Context, r *http.Request) (*Claims, error) {
	if err := v.start(ctx); err != nil {
		return nil, fmt.Errorf("jwks init: %w", err)
	}

	raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if raw == "" {
		return nil, fmt.Errorf("missing bearer token")
	}

	keySet, err := v.cache.Get(ctx, v.jwksURL)
	if err != nil {
		return nil, fmt.Errorf("get jwks: %w", err)
	}

	tok, err := jwt.Parse([]byte(raw),
		jwt.WithKeySet(keySet),
		jwt.WithValidate(true),
		jwt.WithIssuer(v.issuer),
	)
	if err != nil {
		return nil, fmt.Errorf("parse jwt: %w", err)
	}

	claims := &Claims{
		Subject: tok.Subject(),
		Raw:     tok,
	}

	// The act claim (RFC 8693) is a JSON object with at least a "sub" field.
	// jwx exposes private claims as map[string]any.
	if act, ok := tok.PrivateClaims()["act"]; ok {
		if actMap, ok := act.(map[string]any); ok {
			if sub, ok := actMap["sub"].(string); ok && sub != "" {
				claims.DelegatedBy = sub
				claims.IsAgent = true
			}
		}
	}

	return claims, nil
}

// BearerToken extracts the raw token string from the Authorization header,
// returning empty string if the header is absent or not a Bearer scheme.
func BearerToken(r *http.Request) string {
	v := r.Header.Get("Authorization")
	if !strings.HasPrefix(v, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(v, "Bearer ")
}

// unauthenticatedPaths are exempt from JWT validation so that liveness checks
// and other infrastructure calls work without credentials.
var unauthenticatedPaths = map[string]bool{
	"/api/v1/health": true,
	"/api/v1/config": true,
}

// authnMiddleware validates the JWT and stores Claims in the request context.
// Requests without a valid token receive 401. Downstream handlers and the
// Authorizer retrieve claims via ClaimsFromContext.
// Paths in unauthenticatedPaths are exempt and pass through without a token.
func authnMiddleware(v *JWKSValidator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if unauthenticatedPaths[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}
			claims, err := v.Validate(r.Context(), r)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			ctx := context.WithValue(r.Context(), claimsKey{}, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
