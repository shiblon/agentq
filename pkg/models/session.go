package models

import (
	"fmt"
	"math/rand"
	"time"
)

// generateID returns a random 64-bit hex string suitable for use as an ID.
func generateID() string {
	return fmt.Sprintf("%016x", rand.Uint64())
}

// Session represents a user-initiated workflow with session context.
// It tracks artifacts produced, and provides context for routing decisions.
type Session struct {
	ID              string         `json:"id"`
	UserID          string         `json:"user_id"`                    // who initiated this
	Prompt          string         `json:"prompt"`                     // the initial user request
	ParentSessionID string         `json:"parent_session_id,omitempty"` // set when continued from another session
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	Status          string         `json:"status"`    // pending, in_progress, completed, failed
	Artifacts       []Artifact     `json:"artifacts"` // artifacts produced so far
	Metadata        map[string]any `json:"metadata"`  // arbitrary session data
}

// NewSession creates a new session for a user prompt.
func NewSession(userID, prompt string) *Session {
	return &Session{
		ID:        generateID(),
		UserID:    userID,
		Prompt:    prompt,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Status:    "pending",
		Artifacts: []Artifact{},
		Metadata:  make(map[string]any),
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
		ID:        generateID(),
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
		ID:              generateID(),
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
