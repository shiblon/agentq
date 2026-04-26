package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

// testKeyServer generates an RSA key pair, signs tokens with the private key,
// and serves the public JWKS at /jwks. Callers use Sign to mint tokens.
type testKeyServer struct {
	server  *httptest.Server
	privKey jwk.Key
	pubSet  jwk.Set
}

func newTestKeyServer(t *testing.T) *testKeyServer {
	t.Helper()

	raw, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}

	priv, err := jwk.FromRaw(raw)
	if err != nil {
		t.Fatalf("jwk from private key: %v", err)
	}
	if err := priv.Set(jwk.KeyIDKey, "test-key-1"); err != nil {
		t.Fatalf("set key id: %v", err)
	}
	if err := priv.Set(jwk.AlgorithmKey, jwa.RS256); err != nil {
		t.Fatalf("set algorithm: %v", err)
	}

	pub, err := priv.PublicKey()
	if err != nil {
		t.Fatalf("extract public key: %v", err)
	}
	pubSet := jwk.NewSet()
	if err := pubSet.AddKey(pub); err != nil {
		t.Fatalf("add public key to set: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(pubSet); err != nil {
			http.Error(w, "encode error", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)

	return &testKeyServer{server: srv, privKey: priv, pubSet: pubSet}
}

// Sign mints a signed JWT with the given builder options applied.
func (ks *testKeyServer) Sign(t *testing.T, opts ...jwt.Option) string {
	t.Helper()
	tok, err := jwt.NewBuilder().
		Issuer(ks.server.URL).
		Subject("test-subject").
		Expiration(time.Now().Add(time.Hour)).
		Build()
	if err != nil {
		t.Fatalf("build token: %v", err)
	}
	// Apply caller overrides after building so they can replace fields.
	for _, o := range opts {
		_ = o // options applied via WithClaim below -- use custom builder pattern instead
	}
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.RS256, ks.privKey))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return string(signed)
}

// signWithClaims builds a token with the given claims map merged in.
func (ks *testKeyServer) signWithClaims(t *testing.T, sub, issuer string, extra map[string]any) string {
	t.Helper()
	b := jwt.NewBuilder().
		Issuer(issuer).
		Subject(sub).
		Expiration(time.Now().Add(time.Hour))
	for k, v := range extra {
		b = b.Claim(k, v)
	}
	tok, err := b.Build()
	if err != nil {
		t.Fatalf("build token: %v", err)
	}
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.RS256, ks.privKey))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return string(signed)
}

func newValidatorForServer(ks *testKeyServer) *JWKSValidator {
	return NewJWKSValidator(ks.server.URL, ks.server.URL)
}

func requestWithBearer(token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

func TestValidate_HumanToken(t *testing.T) {
	ks := newTestKeyServer(t)
	v := newValidatorForServer(ks)

	token := ks.signWithClaims(t, "human-123", ks.server.URL, nil)
	claims, err := v.Validate(context.Background(), requestWithBearer(token))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claims.Subject != "human-123" {
		t.Errorf("subject = %q, want %q", claims.Subject, "human-123")
	}
	if claims.IsAgent {
		t.Error("IsAgent should be false for human token")
	}
	if claims.DelegatedBy != "" {
		t.Errorf("DelegatedBy = %q, want empty", claims.DelegatedBy)
	}
}

func TestValidate_AgentToken(t *testing.T) {
	ks := newTestKeyServer(t)
	v := newValidatorForServer(ks)

	// Agent token: sub is the agent machine user, act.sub is the delegating human.
	token := ks.signWithClaims(t, "agent-machine-456", ks.server.URL, map[string]any{
		"act": map[string]any{
			"sub": "human-123",
			"iss": ks.server.URL,
		},
	})
	claims, err := v.Validate(context.Background(), requestWithBearer(token))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claims.Subject != "agent-machine-456" {
		t.Errorf("subject = %q, want %q", claims.Subject, "agent-machine-456")
	}
	if !claims.IsAgent {
		t.Error("IsAgent should be true when act claim is present")
	}
	if claims.DelegatedBy != "human-123" {
		t.Errorf("DelegatedBy = %q, want %q", claims.DelegatedBy, "human-123")
	}
}

func TestValidate_MissingToken(t *testing.T) {
	ks := newTestKeyServer(t)
	v := newValidatorForServer(ks)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	_, err := v.Validate(context.Background(), r)
	if err == nil {
		t.Error("expected error for missing token")
	}
}

func TestValidate_WrongIssuer(t *testing.T) {
	ks := newTestKeyServer(t)
	v := newValidatorForServer(ks)

	token := ks.signWithClaims(t, "human-123", "https://evil.example.com", nil)
	_, err := v.Validate(context.Background(), requestWithBearer(token))
	if err == nil {
		t.Error("expected error for wrong issuer")
	}
}

func TestValidate_ExpiredToken(t *testing.T) {
	ks := newTestKeyServer(t)
	v := newValidatorForServer(ks)

	b := jwt.NewBuilder().
		Issuer(ks.server.URL).
		Subject("human-123").
		Expiration(time.Now().Add(-time.Hour))
	tok, _ := b.Build()
	signed, _ := jwt.Sign(tok, jwt.WithKey(jwa.RS256, ks.privKey))

	_, err := v.Validate(context.Background(), requestWithBearer(string(signed)))
	if err == nil {
		t.Error("expected error for expired token")
	}
}

func TestClaimsFromContext(t *testing.T) {
	want := &Claims{Subject: "someone", IsAgent: false}
	ctx := context.WithValue(context.Background(), claimsKey{}, want)
	got := ClaimsFromContext(ctx)
	if got != want {
		t.Errorf("ClaimsFromContext = %v, want %v", got, want)
	}
}

func TestClaimsFromContext_Missing(t *testing.T) {
	got := ClaimsFromContext(context.Background())
	if got != nil {
		t.Errorf("ClaimsFromContext on empty context = %v, want nil", got)
	}
}

func TestAuthnMiddleware_HealthBypass(t *testing.T) {
	ks := newTestKeyServer(t)
	v := newValidatorForServer(ks)

	reached := false
	handler := authnMiddleware(v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if !reached {
		t.Error("health endpoint should bypass authn and reach handler")
	}
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestAuthnMiddleware_UnauthorizedWithoutToken(t *testing.T) {
	ks := newTestKeyServer(t)
	v := newValidatorForServer(ks)

	handler := authnMiddleware(v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestAuthnMiddleware_ClaimsStoredInContext(t *testing.T) {
	ks := newTestKeyServer(t)
	v := newValidatorForServer(ks)

	var gotClaims *Claims
	handler := authnMiddleware(v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotClaims = ClaimsFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	token := ks.signWithClaims(t, "human-123", ks.server.URL, nil)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if gotClaims == nil {
		t.Fatal("claims should be stored in context")
	}
	if gotClaims.Subject != "human-123" {
		t.Errorf("subject = %q, want %q", gotClaims.Subject, "human-123")
	}
}
