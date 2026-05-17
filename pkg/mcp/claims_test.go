package mcp

import (
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
)

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
		Issuer:        "agentq",
		SessionID:     "sess-123",
		Workdir:       "/var/agentq/sessions/sess-123",
		ToolAllowlist: []string{"read_file", "write_file", "list_directory"},
		Expiry:        time.Now().Add(30 * time.Minute).Truncate(time.Second),
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
	if len(got.ToolAllowlist) != len(want.ToolAllowlist) {
		t.Fatalf("ToolAllowlist length = %d, want %d", len(got.ToolAllowlist), len(want.ToolAllowlist))
	}
	for i, tool := range want.ToolAllowlist {
		if got.ToolAllowlist[i] != tool {
			t.Errorf("ToolAllowlist[%d] = %q, want %q", i, got.ToolAllowlist[i], tool)
		}
	}
	if !got.Expiry.Equal(want.Expiry) {
		t.Errorf("Expiry = %v, want %v", got.Expiry, want.Expiry)
	}
}

func TestMintParse_EmptyToolAllowlist(t *testing.T) {
	priv, pubSet := testKeyPair(t)

	raw, err := Mint(priv, Claims{
		Issuer:        "agentq",
		SessionID:     "sess-1",
		Workdir:       "/work",
		ToolAllowlist: []string{},
	})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	got, err := Parse(pubSet, "agentq", raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got.ToolAllowlist) != 0 {
		t.Errorf("ToolAllowlist = %v, want empty", got.ToolAllowlist)
	}
}

func TestMintParse_EmptyWorkdir(t *testing.T) {
	priv, pubSet := testKeyPair(t)

	raw, err := Mint(priv, Claims{
		Issuer:        "agentq",
		SessionID:     "sess-1",
		Workdir:       "",
		ToolAllowlist: []string{"read_file"},
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
		Issuer:        "agentq",
		SessionID:     "sess-1",
		Workdir:       "/work",
		ToolAllowlist: []string{},
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

func TestParse_WrongIssuer(t *testing.T) {
	priv, pubSet := testKeyPair(t)

	raw, err := Mint(priv, Claims{
		Issuer:        "agentq",
		SessionID:     "sess-1",
		Workdir:       "/work",
		ToolAllowlist: []string{},
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
		Issuer:        "agentq",
		SessionID:     "sess-1",
		Workdir:       "/work",
		ToolAllowlist: []string{},
		Expiry:        time.Now().Add(-time.Minute),
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
		Issuer:        "agentq",
		SessionID:     "sess-1",
		Workdir:       "/work",
		ToolAllowlist: []string{},
	})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	_, err = Parse(otherPubSet, "agentq", raw)
	if err == nil {
		t.Error("expected error for wrong verification key")
	}
}
