package models

import (
	"time"
)

// Task represents a unit of work dispatched to an agent queue.
type Task struct {
	ID           string         `json:"id"`
	Queue        string         `json:"queue"`              // target agent queue
	SessionURI   string         `json:"session_uri"`        // doc: URI for the session in eq
	ReplyTo      string         `json:"reply_to,omitempty"` // return address for result; if empty, worker uses its configured default
	Payload      map[string]any `json:"payload"`            // agent-specific instruction data
	CreatedAt    time.Time      `json:"created_at"`
	AttemptCount int            `json:"attempt_count"`
}

// TaskOption configures optional envelope fields on a Task.
type TaskOption func(*Task)

// WithReplyTo sets the return address for the task result. The worker posts
// the result to this queue instead of its configured default reply queue.
// Use this for session-specific routing (e.g. each user session has its own
// result queue that the UI or CLI polls).
func WithReplyTo(queue string) TaskOption {
	return func(t *Task) { t.ReplyTo = queue }
}

// NewTask creates a task for a given agent queue.
func NewTask(queue, sessionURI string, payload map[string]any, opts ...TaskOption) *Task {
	t := &Task{
		ID:         mustGenerateID(),
		Queue:      queue,
		SessionURI: sessionURI,
		Payload:    payload,
		CreatedAt:  time.Now(),
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}
