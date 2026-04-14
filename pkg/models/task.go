package models

import (
	"time"
)

// Task represents a unit of work to be done by an agent.
// It carries the instruction, session context, and configuration snapshot.
type Task struct {
	ID           string          `json:"id"`
	Queue        string          `json:"queue"`       // target agent queue
	SessionURI   string          `json:"session_uri"` // doc: URI for the session in eq
	Payload      map[string]any  `json:"payload"`     // instruction/data
	ConfigRef    *ConfigSnapshot `json:"config_ref"`  // snapshot of relevant config
	CreatedAt    time.Time       `json:"created_at"`
	AttemptCount int             `json:"attempt_count"`
}

// NewTask creates a task for a given agent queue.
func NewTask(queue, sessionURI string, payload map[string]any) *Task {
	return &Task{
		ID:         generateID(),
		Queue:      queue,
		SessionURI: sessionURI,
		Payload:    payload,
		CreatedAt:  time.Now(),
	}
}

// ConfigSnapshot captures agent config at the time of task creation.
// This ensures reproducibility: agent sees what was intended, not live config changes.
type ConfigSnapshot struct {
	AgentName    string            `json:"agent_name"`
	RoutingRules map[string][]Rule `json:"routing_rules,omitempty"` // queue -> rules (for mocks)
	Metadata     map[string]any    `json:"metadata,omitempty"`
}

// Rule represents a mock agent's decision rule.
// For real agents, we'd just have queue mappings; mocks need decision logic.
type Rule struct {
	Condition string `json:"condition"` // e.g., "contains:code", "equals:review"
	Action    string `json:"action"`    // e.g., "enqueue:coder", "enqueue:reviewer"
	Queue     string `json:"queue"`     // target queue if action enqueues
}
