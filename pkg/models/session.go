package models

import "time"

// Session represents a user-initiated workflow with session context.
// It tracks artifacts produced, and provides context for routing decisions.
type Session struct {
	ID        string       `json:"id"`
	UserID    string       `json:"user_id"`       // who initiated this
	Prompt    string       `json:"prompt"`        // the initial user request
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
	Status    string       `json:"status"`        // pending, in_progress, completed, failed
	Artifacts []Artifact   `json:"artifacts"`     // artifacts produced so far
	Metadata  map[string]any `json:"metadata"`    // arbitrary session data
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
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	AgentName string    `json:"agent_name"`
	Type      string    `json:"type"`        // e.g., "dispatch", "analysis", "feedback"
	Path      string    `json:"path"`        // relative path in session directory
	Content   string    `json:"content"`     // for small artifacts, inline
	CreatedAt time.Time `json:"created_at"`
	Metadata  map[string]any `json:"metadata"` // agent-specific metadata
}

// NewArtifact creates an artifact for a session.
func NewArtifact(sessionID, agentName, artifactType, content string) *Artifact {
	ts := time.Now()
	// Path follows the convention: {sessionID}/{timestamp}-{agent}-{type}.md
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

// formatArtifactPath formats the artifact path.
func formatArtifactPath(sessionID string, ts time.Time, agent, aType string) string {
	// Format: sessions/{sessionID}/{ts}-{agent}-{type}.md
	tstamp := ts.Format("20060102-150405")
	return "sessions/" + sessionID + "/" + tstamp + "-" + agent + "-" + aType + ".md"
}
