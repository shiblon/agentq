package approval

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log"
	"strings"

	macaroon "gopkg.in/macaroon.v2"
)

// randID returns a cryptographically random 64-bit hex string for dispatch IDs.
func randID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		log.Fatalf("approval: crypto/rand read failed: %v", err)
	}
	return fmt.Sprintf("%016x", binary.BigEndian.Uint64(b[:]))
}

const (
	tokenTypeReceipt = "dispatch_receipt"
	keyAgent         = "agent"
	keyDispatchID    = "dispatch_id"
)

// ReceiptIssuer mints dispatch receipts. Held by the supervisor; a receipt is
// placed in every outgoing agent task so the worker can present it on return.
// The same root key may back both the ReceiptIssuer and approval.Issuer --
// the type caveat distinguishes the two token kinds.
type ReceiptIssuer struct {
	iss *Issuer
}

// NewReceiptIssuer creates a ReceiptIssuer from a base64-encoded root key.
func NewReceiptIssuer(base64Key, location string) (*ReceiptIssuer, error) {
	iss, err := NewIssuerFromBase64(base64Key, location)
	if err != nil {
		return nil, err
	}
	return &ReceiptIssuer{iss: iss}, nil
}

// DispatchReceipt is a minted receipt for a specific dispatch.
type DispatchReceipt struct {
	token *Token
	// DispatchID is the unique identifier for this dispatch, embedded in the token.
	DispatchID string
}

// Serialize encodes the receipt as a base64 string for a task payload field.
func (r *DispatchReceipt) Serialize() (string, error) {
	return r.token.Serialize()
}

// Mint creates a dispatch receipt for the given session, agent, and a fresh
// dispatch ID (derived from crypto/rand via mustGenerateID). The receipt is
// placed in the outgoing task payload; the exec worker carries it back in the
// return task.
func (ri *ReceiptIssuer) Mint(sessionID, agentName string) (*DispatchReceipt, error) {
	dispatchID := randID()
	id := []byte("agentq-receipt:" + dispatchID)

	m, err := macaroon.New(ri.iss.rootKey, id, ri.iss.location, macaroon.LatestVersion)
	if err != nil {
		return nil, fmt.Errorf("receipt: mint: %w", err)
	}
	for _, cav := range []string{
		keyType + caveatSep + tokenTypeReceipt,
		keySession + caveatSep + sessionID,
		keyAgent + caveatSep + agentName,
		keyDispatchID + caveatSep + dispatchID,
	} {
		if err := m.AddFirstPartyCaveat([]byte(cav)); err != nil {
			return nil, fmt.Errorf("receipt: add caveat: %w", err)
		}
	}
	return &DispatchReceipt{token: &Token{m: m}, DispatchID: dispatchID}, nil
}

// ReceiptVerifier checks dispatch receipts presented in return tasks.
type ReceiptVerifier struct {
	ver *Verifier
}

// NewReceiptVerifier creates a ReceiptVerifier from a base64-encoded root key.
func NewReceiptVerifier(base64Key string) (*ReceiptVerifier, error) {
	ver, err := NewVerifierFromBase64(base64Key)
	if err != nil {
		return nil, err
	}
	return &ReceiptVerifier{ver: ver}, nil
}

// VerifiedReceipt holds the claims extracted from a successfully verified receipt.
type VerifiedReceipt struct {
	SessionID  string
	AgentName  string
	DispatchID string
}

// Verify checks that the serialized receipt is valid for the given session and
// agent. Returns the verified claims on success.
func (rv *ReceiptVerifier) Verify(serialized, sessionID, agentName string) (*VerifiedReceipt, error) {
	if serialized == "" {
		return nil, fmt.Errorf("receipt: no token present")
	}
	b, err := decodeBase64(serialized)
	if err != nil {
		return nil, fmt.Errorf("receipt: decode: %w", err)
	}
	m := &macaroon.Macaroon{}
	if err := m.UnmarshalBinary(b); err != nil {
		return nil, fmt.Errorf("receipt: unmarshal: %w", err)
	}

	var parsed VerifiedReceipt
	check := func(cav string) error {
		k, v, ok := strings.Cut(cav, caveatSep)
		if !ok {
			return fmt.Errorf("malformed caveat %q", cav)
		}
		switch k {
		case keyType:
			if v != tokenTypeReceipt {
				return fmt.Errorf("wrong token type %q (expected %q)", v, tokenTypeReceipt)
			}
		case keySession:
			if v != sessionID {
				return fmt.Errorf("session mismatch: receipt is for %q", v)
			}
			parsed.SessionID = v
		case keyAgent:
			if v != agentName {
				return fmt.Errorf("agent mismatch: receipt is for %q, got %q", v, agentName)
			}
			parsed.AgentName = v
		case keyDispatchID:
			parsed.DispatchID = v
		default:
			return fmt.Errorf("unknown caveat %q", k)
		}
		return nil
	}
	if err := m.Verify(rv.ver.rootKey, check, nil); err != nil {
		return nil, fmt.Errorf("receipt: %w", err)
	}
	return &parsed, nil
}
