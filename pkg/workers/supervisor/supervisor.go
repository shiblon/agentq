package supervisor

import (
	"context"
	"fmt"
	"strings"

	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/entroq"
)

// Supervisor is the orchestrator mock. It decides which agents to enqueue based on:
// - The user's initial prompt
// - Artifacts produced so far in the session
type Supervisor struct {
	client  *entroq.EntroQ
	config  *models.AgentConfig
	session *models.Session
}

// New creates a new supervisor instance.
func New(client *entroq.EntroQ, config *models.AgentConfig) *Supervisor {
	return &Supervisor{
		client: client,
		config: config,
	}
}

// ProcessTask handles an incoming supervisor task.
// It reads the session, applies routing logic, and enqueues follow-ups.
func (s *Supervisor) ProcessTask(ctx context.Context, task *entroq.Task) ([]entroq.ModifyArg, error) {
	sessionID := task.ID
	// Task.Value is the prompt bytes
	payload := string(task.Value)

	// In a real system, load session from persistent storage.
	// For now, we mock it.
	session := &models.Session{
		ID:     sessionID,
		Prompt: payload,
	}
	s.session = session

	// Apply routing rules: determine which agents to enqueue.
	agents := s.route(payload)

	// Write a dispatch artifact documenting what we decided.
	dispatchArtifact := models.NewArtifact(
		sessionID,
		"supervisor",
		"dispatch",
		fmt.Sprintf("Routing analysis:\nPrompt: %s\nDecided agents: %v\n", payload, agents),
	)
	_ = dispatchArtifact // suppress unused warning for now

	// Enqueue tasks for each agent.
	mods := []entroq.ModifyArg{}
	for _, agent := range agents {
		// Queue selection: get the output queue for this agent
		outputQueue := s.config.GetOutputQueue(agent)
		if outputQueue == "" {
			return nil, fmt.Errorf("no output queue configured for agent: %s", agent)
		}

		// For now, just log what we'd do.
		// Real implementation would enqueue via client.Modify().
		fmt.Printf("➜ Would enqueue task for agent: %s (queue: %s)\n", agent, outputQueue)
	}

	// Delete this task (we've processed it).
	mods = append(mods, task.Delete())

	return mods, nil
}

// route applies routing rules to the prompt.
// Mock implementation: simple keyword matching.
func (s *Supervisor) route(prompt string) []string {
	lower := strings.ToLower(prompt)
	agents := []string{}

	// Simple mock routing:
	if strings.Contains(lower, "code") || strings.Contains(lower, "implement") {
		agents = append(agents, "coder")
	}
	if strings.Contains(lower, "review") || strings.Contains(lower, "check") {
		agents = append(agents, "reviewer")
	}
	if strings.Contains(lower, "research") || strings.Contains(lower, "investigate") {
		agents = append(agents, "researcher")
	}

	// Always dispatch to at least one agent (or fail if unclear)
	if len(agents) == 0 {
		agents = append(agents, "coder") // default fallback
	}

	return agents
}
