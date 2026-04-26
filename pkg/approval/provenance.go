package approval

import (
	"fmt"
	"strings"
	"time"

	macaroon "gopkg.in/macaroon.v2"
)

const (
	tokenTypeProvenance = "provenance"
	keyType             = "type"
	keyParentSession    = "parent_session"
	keyCreatedAt        = "created_at"
)

// ProvenanceIssuer mints session provenance tokens. It is held by the API
// server; workers and the supervisor never have access to it. A valid
// provenance token proves a task originated from a legitimate API submission.
type ProvenanceIssuer struct {
	iss *Issuer
}

// NewProvenanceIssuer creates a ProvenanceIssuer backed by the given base64 key.
func NewProvenanceIssuer(base64Key, location string) (*ProvenanceIssuer, error) {
	iss, err := NewIssuerFromBase64(base64Key, location)
	if err != nil {
		return nil, err
	}
	return &ProvenanceIssuer{iss: iss}, nil
}

// MintSession mints a provenance token for a new session.
func (p *ProvenanceIssuer) MintSession(sessionID string) (*Token, error) {
	id := []byte("agentq-provenance:" + sessionID)
	m, err := macaroon.New(p.iss.rootKey, id, p.iss.location, macaroon.LatestVersion)
	if err != nil {
		return nil, fmt.Errorf("provenance: mint: %w", err)
	}
	for _, cav := range []string{
		keyType + caveatSep + tokenTypeProvenance,
		keySession + caveatSep + sessionID,
		keyCreatedAt + caveatSep + fmt.Sprintf("%d", time.Now().Unix()),
	} {
		if err := m.AddFirstPartyCaveat([]byte(cav)); err != nil {
			return nil, fmt.Errorf("provenance: add caveat: %w", err)
		}
	}
	return &Token{m: m}, nil
}

// MintContinuation mints a provenance token for a session that continues from
// a parent. The parent's provenance token is verified before minting, ensuring
// the continuation chain traces back to a legitimate API submission.
func (p *ProvenanceIssuer) MintContinuation(sessionID, parentSessionID, parentToken string) (*Token, error) {
	v := &ProvenanceVerifier{ver: NewVerifier(p.iss.rootKey)}
	if err := v.Verify(parentToken, parentSessionID); err != nil {
		return nil, fmt.Errorf("provenance: parent token invalid: %w", err)
	}
	id := []byte("agentq-provenance:" + sessionID)
	m, err := macaroon.New(p.iss.rootKey, id, p.iss.location, macaroon.LatestVersion)
	if err != nil {
		return nil, fmt.Errorf("provenance: mint continuation: %w", err)
	}
	for _, cav := range []string{
		keyType + caveatSep + tokenTypeProvenance,
		keySession + caveatSep + sessionID,
		keyParentSession + caveatSep + parentSessionID,
		keyCreatedAt + caveatSep + fmt.Sprintf("%d", time.Now().Unix()),
	} {
		if err := m.AddFirstPartyCaveat([]byte(cav)); err != nil {
			return nil, fmt.Errorf("provenance: add caveat: %w", err)
		}
	}
	return &Token{m: m}, nil
}

// ProvenanceVerifier checks session provenance tokens. Held by the supervisor;
// it verifies that every task it processes originated from a legitimate API
// submission.
type ProvenanceVerifier struct {
	ver *Verifier
}

// NewProvenanceVerifier creates a ProvenanceVerifier from a base64 key.
func NewProvenanceVerifier(base64Key string) (*ProvenanceVerifier, error) {
	ver, err := NewVerifierFromBase64(base64Key)
	if err != nil {
		return nil, err
	}
	return &ProvenanceVerifier{ver: ver}, nil
}

// Verify checks that the serialized token is a valid provenance token for the
// given session. Returns nil on success.
func (p *ProvenanceVerifier) Verify(serialized, sessionID string) error {
	if serialized == "" {
		return fmt.Errorf("provenance: no token present")
	}
	b, err := decodeBase64(serialized)
	if err != nil {
		return fmt.Errorf("provenance: decode: %w", err)
	}
	m := &macaroon.Macaroon{}
	if err := m.UnmarshalBinary(b); err != nil {
		return fmt.Errorf("provenance: unmarshal: %w", err)
	}
	check := func(cav string) error {
		k, v, ok := strings.Cut(cav, caveatSep)
		if !ok {
			return fmt.Errorf("malformed caveat %q", cav)
		}
		switch k {
		case keyType:
			if v != tokenTypeProvenance {
				return fmt.Errorf("wrong token type %q", v)
			}
		case keySession:
			if v != sessionID {
				return fmt.Errorf("session mismatch: token is for %q", v)
			}
		case keyParentSession, keyCreatedAt:
			// informational; no enforcement
		default:
			return fmt.Errorf("unknown caveat %q", k)
		}
		return nil
	}
	if err := m.Verify(p.ver.rootKey, check, nil); err != nil {
		return fmt.Errorf("provenance: %w", err)
	}
	return nil
}
