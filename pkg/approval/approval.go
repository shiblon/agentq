// Package approval issues and verifies cryptographically signed approval tokens
// for agentq task dispatch. Tokens are Macaroons (https://research.google/pubs/pub41892/)
// with caveats that encode the session, allowed actions, and expiry.
//
// The supervisor mints a token before dispatching an elevated task. The exec
// worker verifies the token before appending the approval suffix to its command.
// Without a valid token the worker runs without elevated permissions, regardless
// of what the task payload claims.
//
// Third-party caveats (future): the human review service can discharge a caveat
// embedded in the token, making human approval cryptographically required rather
// than conventional.
package approval

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	macaroon "gopkg.in/macaroon.v2"
)

const (
	// caveatSep separates caveat key from value.
	caveatSep = "="

	// keySession is the caveat key for the session ID.
	keySession = "session"

	// keyActions is the caveat key for the allowed action list (comma-separated).
	keyActions = "actions"

	// keyExpiry is the caveat key for the expiry timestamp (Unix seconds).
	keyExpiry = "expires"
)

// Token is a minted approval token that can be serialized into a task payload.
type Token struct {
	m *macaroon.Macaroon
}

// Issuer mints approval tokens using a root key. The root key should be a
// 32-byte random secret held only by the supervisor. Load it from a file or
// Vault secret; never embed it in source.
type Issuer struct {
	rootKey []byte
	location string // identifies this issuer in the token (informational)
}

// NewIssuer creates an Issuer with the given root key.
// rootKey must be at least 16 bytes; 32 bytes recommended.
func NewIssuer(rootKey []byte, location string) (*Issuer, error) {
	if len(rootKey) < 16 {
		return nil, fmt.Errorf("approval: root key must be at least 16 bytes")
	}
	return &Issuer{rootKey: rootKey, location: location}, nil
}

// NewIssuerFromBase64 creates an Issuer from a base64-encoded root key.
// Useful when the key is stored in an env var or secret file.
func NewIssuerFromBase64(encoded, location string) (*Issuer, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return nil, fmt.Errorf("approval: decode root key: %w", err)
	}
	return NewIssuer(key, location)
}

// decodeBase64 is a shared helper used by both Verifier and ProvenanceVerifier.
func decodeBase64(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(strings.TrimSpace(s))
}

// GenerateKey returns a new random 32-byte key encoded as base64.
// Use this once to generate a root key to store in Vault or a secret file.
func GenerateKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("approval: generate key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// Mint creates a new approval token for the given session and actions.
// ttl is how long the token is valid; use a short value (e.g. 10 minutes).
func (iss *Issuer) Mint(sessionID string, actions []string, ttl time.Duration) (*Token, error) {
	// The macaroon identifier embeds the session ID so it is visible without
	// verification -- useful for logging and debugging.
	id := []byte("agentq-approval:" + sessionID)

	m, err := macaroon.New(iss.rootKey, id, iss.location, macaroon.LatestVersion)
	if err != nil {
		return nil, fmt.Errorf("approval: mint: %w", err)
	}

	if err := m.AddFirstPartyCaveat([]byte(keySession + caveatSep + sessionID)); err != nil {
		return nil, fmt.Errorf("approval: add session caveat: %w", err)
	}
	if err := m.AddFirstPartyCaveat([]byte(keyActions + caveatSep + strings.Join(actions, ","))); err != nil {
		return nil, fmt.Errorf("approval: add actions caveat: %w", err)
	}
	expiry := fmt.Sprintf("%d", time.Now().Add(ttl).Unix())
	if err := m.AddFirstPartyCaveat([]byte(keyExpiry + caveatSep + expiry)); err != nil {
		return nil, fmt.Errorf("approval: add expiry caveat: %w", err)
	}

	return &Token{m: m}, nil
}

// Serialize encodes the token as a base64 string suitable for a JSON payload.
func (t *Token) Serialize() (string, error) {
	b, err := t.m.MarshalBinary()
	if err != nil {
		return "", fmt.Errorf("approval: serialize: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// Verifier checks approval tokens minted by an Issuer with the same root key.
type Verifier struct {
	rootKey []byte
}

// NewVerifier creates a Verifier. rootKey must match the Issuer's root key.
func NewVerifier(rootKey []byte) *Verifier {
	return &Verifier{rootKey: rootKey}
}

// NewVerifierFromBase64 creates a Verifier from a base64-encoded root key.
func NewVerifierFromBase64(encoded string) (*Verifier, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return nil, fmt.Errorf("approval: decode verifier key: %w", err)
	}
	return NewVerifier(key), nil
}

// Verified holds the claims extracted from a successfully verified token.
type Verified struct {
	SessionID string
	Actions   []string
}

// Verify parses and verifies a serialized token. Returns the verified claims
// on success, or an error if the token is missing, malformed, expired, or
// the signature does not match the root key.
func (v *Verifier) Verify(serialized, sessionID string) (*Verified, error) {
	if serialized == "" {
		return nil, fmt.Errorf("approval: no token present")
	}

	b, err := base64.StdEncoding.DecodeString(serialized)
	if err != nil {
		return nil, fmt.Errorf("approval: decode token: %w", err)
	}

	m := &macaroon.Macaroon{}
	if err := m.UnmarshalBinary(b); err != nil {
		return nil, fmt.Errorf("approval: unmarshal token: %w", err)
	}

	// Build the caveat checker. All caveats must be satisfied.
	now := time.Now().Unix()
	var parsed Verified

	check := func(cav string) error {
		k, val, ok := strings.Cut(cav, caveatSep)
		if !ok {
			return fmt.Errorf("malformed caveat %q", cav)
		}
		switch k {
		case keySession:
			if val != sessionID {
				return fmt.Errorf("session mismatch: token is for %q, got %q", val, sessionID)
			}
			parsed.SessionID = val
		case keyActions:
			parsed.Actions = strings.Split(val, ",")
		case keyExpiry:
			var exp int64
			if _, err := fmt.Sscanf(val, "%d", &exp); err != nil {
				return fmt.Errorf("invalid expiry %q", val)
			}
			if now > exp {
				return fmt.Errorf("token expired at %d (now %d)", exp, now)
			}
		default:
			return fmt.Errorf("unknown caveat %q", k)
		}
		return nil
	}

	if err := m.Verify(v.rootKey, check, nil); err != nil {
		return nil, fmt.Errorf("approval: verification failed: %w", err)
	}

	return &parsed, nil
}
