package mcp

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"os"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
)

// Algorithm identifies the signing algorithm for key generation.
type Algorithm string

const (
	AlgES256 Algorithm = "ES256" // ECDSA P-256 (default, recommended)
	AlgRS256 Algorithm = "RS256" // RSA 2048 (for BYOK compatibility)
)

// KeyPair holds a private signing key and the matching public key set.
// Both are ready for use with Mint and the MCP server's PublicKeys config.
type KeyPair struct {
	Private jwk.Key
	Public  jwk.Set
}

// GenerateKeyPair generates a new signing key pair for the given algorithm.
// ES256 is recommended; RS256 is available for environments that require RSA.
func GenerateKeyPair(alg Algorithm) (*KeyPair, error) {
	var rawPriv any
	var err error

	switch alg {
	case AlgES256:
		rawPriv, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case AlgRS256:
		rawPriv, err = rsa.GenerateKey(rand.Reader, 2048)
	default:
		return nil, fmt.Errorf("unsupported algorithm %q (use ES256 or RS256)", alg)
	}
	if err != nil {
		return nil, fmt.Errorf("generate %s key: %w", alg, err)
	}

	priv, err := jwk.FromRaw(rawPriv)
	if err != nil {
		return nil, fmt.Errorf("wrap private key: %w", err)
	}
	if err := priv.Set(jwk.AlgorithmKey, jwa.SignatureAlgorithm(alg)); err != nil {
		return nil, fmt.Errorf("set algorithm: %w", err)
	}
	if err := priv.Set(jwk.KeyIDKey, uuid.NewString()); err != nil {
		return nil, fmt.Errorf("set key id: %w", err)
	}

	pub, err := priv.PublicKey()
	if err != nil {
		return nil, fmt.Errorf("extract public key: %w", err)
	}
	pubSet := jwk.NewSet()
	if err := pubSet.AddKey(pub); err != nil {
		return nil, fmt.Errorf("build public key set: %w", err)
	}

	return &KeyPair{Private: priv, Public: pubSet}, nil
}

// GenerateEphemeralKey generates a throwaway ES256 key pair for use with
// --insecure-no-keys. The key is not persisted anywhere.
func GenerateEphemeralKey() (*KeyPair, error) {
	return GenerateKeyPair(AlgES256)
}

// WritePrivateKey serialises kp.Private to a JWK JSON file at path.
// Written with mode 0600 (owner read/write only).
func WritePrivateKey(path string, kp *KeyPair) error {
	data, err := json.MarshalIndent(kp.Private, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal private key: %w", err)
	}
	return os.WriteFile(path, data, 0600)
}

// WritePublicKeySet serialises kp.Public to a JWKS JSON file at path.
func WritePublicKeySet(path string, kp *KeyPair) error {
	data, err := json.MarshalIndent(kp.Public, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal public key set: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// LoadPrivateKey reads a JWK JSON file and returns the first private key.
// Supports both ES256 and RS256 keys -- the algorithm is read from the key itself.
func LoadPrivateKey(path string) (jwk.Key, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read key file %s: %w", path, err)
	}
	set, err := jwk.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse key file %s: %w", path, err)
	}
	key, ok := set.Key(0)
	if !ok {
		return nil, fmt.Errorf("key file %s: no keys found", path)
	}
	return key, nil
}
