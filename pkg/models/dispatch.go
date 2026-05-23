package models

// DispatchStatus indicates the outcome of a dispatched agent task.
type DispatchStatus string

const (
	DispatchCompleted DispatchStatus = "completed"
	DispatchFailed    DispatchStatus = "failed"
)

// ArtifactRef is a lightweight pointer to an artifact produced by an agent.
type ArtifactRef struct {
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	URI       string `json:"uri"`
}

// DispatchResult is the structured outcome of a completed agent dispatch.
// It is stored in the dispatch_complete chunk and becomes the content of the
// supervisor's tool_result block when the transcript is replayed.
type DispatchResult struct {
	AgentName     string         `json:"agent_name"`
	SessionID     string         `json:"session_id"`
	Status        DispatchStatus `json:"status"`
	Summary       string         `json:"summary"`
	Artifacts     []ArtifactRef  `json:"artifacts,omitempty"`
	TranscriptURI string         `json:"transcript_uri,omitempty"`
	Error         string         `json:"error,omitempty"`
}
