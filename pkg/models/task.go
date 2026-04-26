package models

import (
	"time"
)

// Task represents a unit of work dispatched to an agent queue.
type Task struct {
	ID         string         `json:"id"`
	Queue      string         `json:"queue"`       // target agent queue
	SessionURI string         `json:"session_uri"` // doc: URI for the session in eq
	Payload    map[string]any `json:"payload"`     // agent-specific instruction data
	CreatedAt  time.Time      `json:"created_at"`
	AttemptCount int          `json:"attempt_count"`
}

// NewTask creates a task for a given agent queue.
func NewTask(queue, sessionURI string, payload map[string]any) *Task {
	return &Task{
		ID:         mustGenerateID(),
		Queue:      queue,
		SessionURI: sessionURI,
		Payload:    payload,
		CreatedAt:  time.Now(),
	}
}
