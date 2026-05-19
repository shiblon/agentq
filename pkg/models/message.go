package models

// Message is one turn in a conversation transcript.
type Message struct {
	// Role is "system", "user", or "assistant".
	Role    string `json:"role"`
	Content string `json:"content"`

	// Provenance -- optional, for tracing which agent/step produced this message.
	Agent string `json:"agent,omitempty"`
	Step  int    `json:"step,omitempty"`
}
