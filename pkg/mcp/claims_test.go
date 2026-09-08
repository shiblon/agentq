package mcp

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
)

// base64RawURL encodes b as base64url with no padding, matching the JWT payload encoding.
func base64RawURL(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

// testKeyPair generates an RSA key pair and returns the private jwk.Key and a
// jwk.Set containing only the public key, mirroring the real issuer/verifier split.
func testKeyPair(t *testing.T) (priv jwk.Key, pubSet jwk.Set) {
	t.Helper()
	raw, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	priv, err = jwk.FromRaw(raw)
	if err != nil {
		t.Fatalf("jwk from private: %v", err)
	}
	if err := priv.Set(jwk.AlgorithmKey, jwa.RS256); err != nil {
		t.Fatalf("set algorithm: %v", err)
	}
	if err := priv.Set(jwk.KeyIDKey, "test-key"); err != nil {
		t.Fatalf("set kid: %v", err)
	}
	pub, err := priv.PublicKey()
	if err != nil {
		t.Fatalf("extract public key: %v", err)
	}
	pubSet = jwk.NewSet()
	if err := pubSet.AddKey(pub); err != nil {
		t.Fatalf("add public key: %v", err)
	}
	return priv, pubSet
}

func TestMintParse_RoundTrip(t *testing.T) {
	priv, pubSet := testKeyPair(t)

	want := Claims{
		Issuer:    "agentq",
		SessionID: "sess-123",
		Workdir:   "/var/agentq/sessions/sess-123",
		Legs:      Legs(Untrusted, Private),
		Expiry:    time.Now().Add(30 * time.Minute).Truncate(time.Second),
	}

	raw, err := Mint(priv, want)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	got, err := Parse(pubSet, "agentq", raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if got.Issuer != want.Issuer {
		t.Errorf("Issuer = %q, want %q", got.Issuer, want.Issuer)
	}
	if got.SessionID != want.SessionID {
		t.Errorf("SessionID = %q, want %q", got.SessionID, want.SessionID)
	}
	if got.Workdir != want.Workdir {
		t.Errorf("Workdir = %q, want %q", got.Workdir, want.Workdir)
	}
	if got.Legs != want.Legs {
		t.Errorf("Legs = %s, want %s", got.Legs, want.Legs)
	}
	if !got.Expiry.Equal(want.Expiry) {
		t.Errorf("Expiry = %v, want %v", got.Expiry, want.Expiry)
	}
}

func TestMintParse_NoLegs(t *testing.T) {
	priv, pubSet := testKeyPair(t)

	raw, err := Mint(priv, Claims{
		Issuer:    "agentq",
		SessionID: "sess-1",
		Workdir:   "/work",
		Legs:      Legs(),
	})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	got, err := Parse(pubSet, "agentq", raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Legs != Legs() {
		t.Errorf("Legs = %s, want none", got.Legs)
	}
}

func TestMintParse_EmptyWorkdir(t *testing.T) {
	priv, pubSet := testKeyPair(t)

	raw, err := Mint(priv, Claims{
		Issuer:    "agentq",
		SessionID: "sess-1",
		Workdir:   "",
		Legs:      Legs(Untrusted, Private),
	})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	got, err := Parse(pubSet, "agentq", raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Workdir != "" {
		t.Errorf("Workdir = %q, want empty", got.Workdir)
	}
}

func TestMintParse_DefaultExpiry(t *testing.T) {
	priv, pubSet := testKeyPair(t)

	before := time.Now()
	raw, err := Mint(priv, Claims{
		Issuer:    "agentq",
		SessionID: "sess-1",
		Workdir:   "/work",
		Legs:      Legs(),
		// Expiry zero: should default to ~1 hour
	})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	after := time.Now()

	got, err := Parse(pubSet, "agentq", raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// JWTs store expiry as whole seconds (RFC 7519 NumericDate), so truncate
	// the bounds before comparing.
	minExpiry := before.Truncate(time.Second).Add(defaultTokenTTL)
	maxExpiry := after.Truncate(time.Second).Add(defaultTokenTTL)
	if got.Expiry.Before(minExpiry) || got.Expiry.After(maxExpiry) {
		t.Errorf("Expiry = %v, want between %v and %v", got.Expiry, minExpiry, maxExpiry)
	}
}

// -- ParseInsecure tests ------------------------------------------------------

func TestParseInsecure_RoundTrip(t *testing.T) {
	priv, _ := testKeyPair(t)
	want := Claims{
		Issuer:    "agentq",
		SessionID: "sess-insecure",
		Workdir:   "/var/work",
		Legs:      Legs(Untrusted, Private),
	}
	raw, err := Mint(priv, want)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	got, err := ParseInsecure(raw)
	if err != nil {
		t.Fatalf("ParseInsecure: %v", err)
	}
	if got.SessionID != want.SessionID {
		t.Errorf("SessionID = %q, want %q", got.SessionID, want.SessionID)
	}
	if got.Workdir != want.Workdir {
		t.Errorf("Workdir = %q, want %q", got.Workdir, want.Workdir)
	}
	if got.Legs != want.Legs {
		t.Fatalf("Legs = %s, want %s", got.Legs, want.Legs)
	}
}

func TestParseInsecure_AcceptsWrongKey(t *testing.T) {
	// Signed with one key -- ParseInsecure must succeed even though we hold a
	// different key (the whole point of the insecure mode).
	priv, _ := testKeyPair(t)
	raw, err := Mint(priv, Claims{
		Issuer:    "agentq",
		SessionID: "sess-1",
		Workdir:   "/work",
		Legs:      Legs(),
	})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	// Parse with a completely different key set -- must still succeed.
	_, otherPubSet := testKeyPair(t)
	if _, err := Parse(otherPubSet, "agentq", raw); err == nil {
		t.Error("Parse should fail with wrong key (sanity check)")
	}
	if _, err := ParseInsecure(raw); err != nil {
		t.Errorf("ParseInsecure should succeed regardless of key: %v", err)
	}
}

func TestParseInsecure_AcceptsExpired(t *testing.T) {
	priv, _ := testKeyPair(t)
	raw, err := Mint(priv, Claims{
		Issuer:    "agentq",
		SessionID: "sess-1",
		Workdir:   "/work",
		Legs:      Legs(),
		Expiry:    time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	// ParseInsecure doesn't check expiry.
	if _, err := ParseInsecure(raw); err != nil {
		t.Errorf("ParseInsecure should accept expired token: %v", err)
	}
}

func TestParseInsecure_MalformedJWT(t *testing.T) {
	cases := []string{
		"",
		"notajwt",
		"only.two",
		"has.four.parts.here",
	}
	for _, raw := range cases {
		if _, err := ParseInsecure(raw); err == nil {
			t.Errorf("ParseInsecure(%q): expected error for malformed jwt", raw)
		}
	}
}

func TestParseInsecure_MissingClaims(t *testing.T) {
	// Hand-craft JWTs with a missing required claim. ParseInsecure does not
	// verify the signature so a fake sig is fine here.
	header := "eyJhbGciOiJSUzI1NiJ9" // {"alg":"RS256"}

	cases := []struct {
		name    string
		payload string // JSON, base64url-encoded below
	}{
		{
			"missing mcp_session",
			`{"iss":"agentq","mcp_workdir":"/w","mcp_tools":[]}`,
		},
		{
			"missing mcp_workdir",
			`{"iss":"agentq","mcp_session":"s1","mcp_tools":[]}`,
		},
		{
			"missing mcp_tools",
			`{"iss":"agentq","mcp_session":"s1","mcp_workdir":"/w"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enc := base64RawURL([]byte(tc.payload))
			raw := header + "." + enc + ".fakesig"
			if _, err := ParseInsecure(raw); err == nil {
				t.Errorf("expected error for %s", tc.name)
			}
		})
	}
}

func TestParse_WrongIssuer(t *testing.T) {
	priv, pubSet := testKeyPair(t)

	raw, err := Mint(priv, Claims{
		Issuer:    "agentq",
		SessionID: "sess-1",
		Workdir:   "/work",
		Legs:      Legs(),
	})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	_, err = Parse(pubSet, "other-issuer", raw)
	if err == nil {
		t.Error("expected error for wrong issuer")
	}
}

func TestParse_ExpiredToken(t *testing.T) {
	priv, pubSet := testKeyPair(t)

	raw, err := Mint(priv, Claims{
		Issuer:    "agentq",
		SessionID: "sess-1",
		Workdir:   "/work",
		Legs:      Legs(),
		Expiry:    time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	_, err = Parse(pubSet, "agentq", raw)
	if err == nil {
		t.Error("expected error for expired token")
	}
}

func TestParse_BadSignature(t *testing.T) {
	priv, _ := testKeyPair(t)
	_, otherPubSet := testKeyPair(t) // different key pair

	raw, err := Mint(priv, Claims{
		Issuer:    "agentq",
		SessionID: "sess-1",
		Workdir:   "/work",
		Legs:      Legs(),
	})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	_, err = Parse(otherPubSet, "agentq", raw)
	if err == nil {
		t.Error("expected error for wrong verification key")
	}
}
