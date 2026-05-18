package models

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log"
	"time"
)

// generateID returns a cryptographically random 64-bit hex string.
func generateID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%016x", binary.BigEndian.Uint64(b[:])), nil
}

// mustGenerateID calls generateID and fatals if the OS entropy source fails.
// crypto/rand failure indicates a broken system, so continuing is not meaningful.
func mustGenerateID() string {
	id, err := generateID()
	if err != nil {
		log.Fatalf("models: crypto/rand read failed: %v", err)
	}
	return id
}

// SessionMeta holds well-known session metadata fields. Using a typed struct
// rather than map[string]any prevents type-assertion bugs and documents the schema.
type SessionMeta struct {
	// CompactInherited tells the supervisor to summarize inherited artifacts
	// into a single compact_summary on its first dispatch.
	CompactInherited bool `json:"compact_inherited,omitempty"`
	// HumanToken is the raw bearer token from the submitting user, held only
	// long enough for the supervisor to perform RFC 8693 token exchange.
	HumanToken string `json:"human_token,omitempty"`
	// WorkspaceRepo is the target repository path agents should work in.
	WorkspaceRepo string `json:"workspace_repo,omitempty"`
	// ProvenanceToken is a Macaroon minted by the API server at session
	// creation. It proves this session originated from a legitimate API
	// submission and is carried in every supervisor task for the session.
	ProvenanceToken string `json:"provenance_token,omitempty"`
}

// UserReplyQueue returns the deterministic queue name for delivering supervisor
// output back to the user. Derived from the session ID so no stored field is
// needed; any caller that knows the session ID can compute it.
func UserReplyQueue(sessionID string) string {
	return "agentq/sessions/" + sessionID + "/reply"
}

// Session represents a user-initiated workflow with session context.
// It tracks artifacts produced, and provides context for routing decisions.
type Session struct {
	ID              string     `json:"id"`
	UserID          string     `json:"user_id"`
	Prompt          string     `json:"prompt"`
	ParentSessionID string     `json:"parent_session_id,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	Status          string     `json:"status"` // pending, in_progress, completed, failed, cancelled, awaiting_review
	Artifacts       []Artifact `json:"artifacts"`
	Meta            SessionMeta `json:"metadata"` // JSON key kept as "metadata" for wire compat
}

// NewSession creates a new session for a user prompt.
func NewSession(userID, prompt string) *Session {
	return &Session{
		ID:        mustGenerateID(),
		UserID:    userID,
		Prompt:    prompt,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Status:    "pending",
		Artifacts: []Artifact{},
	}
}

// Artifact represents a file-based output from an agent.
// Named: {sessionID}/{timestamp}-{agent}-{type}.{ext}
type Artifact struct {
	ID              string         `json:"id"`
	SessionID       string         `json:"session_id"`
	OriginSessionID string         `json:"origin_session_id,omitempty"` // set when inherited from a parent session
	AgentName       string         `json:"agent_name"`
	Type            string         `json:"type"`    // e.g., "dispatch", "result", "context_summary"
	Path            string         `json:"path"`    // relative path in session directory
	Content         string         `json:"content"` // for small artifacts, inline
	CreatedAt       time.Time      `json:"created_at"`
	Metadata        map[string]any `json:"metadata"` // agent-specific metadata
}

// NewArtifact creates an artifact for a session.
func NewArtifact(sessionID, agentName, artifactType, content string) *Artifact {
	ts := time.Now()
	path := formatArtifactPath(sessionID, ts, agentName, artifactType)
	return &Artifact{
		ID:        mustGenerateID(),
		SessionID: sessionID,
		AgentName: agentName,
		Type:      artifactType,
		Path:      path,
		Content:   content,
		CreatedAt: ts,
		Metadata:  make(map[string]any),
	}
}

// InheritedArtifact creates a copy of an artifact for a new session,
// preserving the origin session ID for chain inspection.
func InheritedArtifact(newSessionID string, src Artifact) Artifact {
	origin := src.OriginSessionID
	if origin == "" {
		origin = src.SessionID
	}
	ts := time.Now()
	return Artifact{
		ID:              mustGenerateID(),
		SessionID:       newSessionID,
		OriginSessionID: origin,
		AgentName:       src.AgentName,
		Type:            src.Type,
		Path:            formatInheritedPath(newSessionID, origin, ts, src.AgentName, src.Type),
		Content:         src.Content,
		CreatedAt:       ts,
		Metadata:        src.Metadata,
	}
}

// formatArtifactPath formats the artifact path for artifacts produced in this session.
func formatArtifactPath(sessionID string, ts time.Time, agent, aType string) string {
	tstamp := ts.Format("20060102-150405")
	return "sessions/" + sessionID + "/" + tstamp + "-" + agent + "-" + aType + ".md"
}

// formatInheritedPath formats the artifact path for artifacts inherited from a parent session.
func formatInheritedPath(sessionID, originID string, ts time.Time, agent, aType string) string {
	tstamp := ts.Format("20060102-150405")
	return "sessions/" + sessionID + "/inherited/" + originID + "/" + tstamp + "-" + agent + "-" + aType + ".md"
}
