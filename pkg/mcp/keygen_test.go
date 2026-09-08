package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateKeyPair_ES256(t *testing.T) {
	kp, err := GenerateKeyPair(AlgES256)
	if err != nil {
		t.Fatalf("GenerateKeyPair(ES256): %v", err)
	}
	if kp.Private == nil || kp.Public == nil {
		t.Fatal("key pair has nil fields")
	}
	roundTripMintParse(t, kp, "es256-issuer")
}

func TestGenerateKeyPair_RS256(t *testing.T) {
	kp, err := GenerateKeyPair(AlgRS256)
	if err != nil {
		t.Fatalf("GenerateKeyPair(RS256): %v", err)
	}
	roundTripMintParse(t, kp, "rs256-issuer")
}

func TestGenerateKeyPair_Unknown(t *testing.T) {
	_, err := GenerateKeyPair("PS512")
	if err == nil {
		t.Error("expected error for unsupported algorithm")
	}
}

func TestGenerateEphemeralKey(t *testing.T) {
	kp, err := GenerateEphemeralKey()
	if err != nil {
		t.Fatalf("GenerateEphemeralKey: %v", err)
	}
	roundTripMintParse(t, kp, "ephemeral-issuer")
}

func TestWriteAndLoadPrivateKey(t *testing.T) {
	kp, err := GenerateKeyPair(AlgES256)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	dir := t.TempDir()
	privPath := filepath.Join(dir, "private.jwk")
	pubPath := filepath.Join(dir, "public.jwks")

	if err := WritePrivateKey(privPath, kp); err != nil {
		t.Fatalf("WritePrivateKey: %v", err)
	}
	if err := WritePublicKeySet(pubPath, kp); err != nil {
		t.Fatalf("WritePublicKeySet: %v", err)
	}

	// private.jwk must be owner-read-only
	info, err := os.Stat(privPath)
	if err != nil {
		t.Fatalf("stat private key: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("private key mode = %o, want 0600", info.Mode().Perm())
	}

	// Load the private key and verify it can still mint JWTs.
	loaded, err := LoadPrivateKey(privPath)
	if err != nil {
		t.Fatalf("LoadPrivateKey: %v", err)
	}
	loadedKP := &KeyPair{Private: loaded, Public: kp.Public}
	roundTripMintParse(t, loadedKP, "loaded-issuer")
}

// roundTripMintParse verifies that kp can mint a JWT and Parse can verify it.
func roundTripMintParse(t *testing.T, kp *KeyPair, issuer string) {
	t.Helper()
	raw, err := Mint(kp.Private, Claims{
		Issuer:    issuer,
		SessionID: "test-session",
		Grants:    GrantSet{{Tool: "read_file", Scope: Scope{Root: "/work"}}, {Tool: "grep", Scope: Scope{Root: "/work"}}, {Tool: "git_log", Scope: Scope{Root: "/work"}}, {Tool: "list_directory", Scope: Scope{Root: "/work"}}},
	})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	got, err := Parse(kp.Public, issuer, raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.SessionID != "test-session" {
		t.Errorf("SessionID = %q, want test-session", got.SessionID)
	}
}
