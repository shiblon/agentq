package models

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/shiblon/entroq"
)

// AgentConfig defines an agent's behavior and queues.
// For real agents: just queue names and basic metadata.
// For mocks: also include routing rules and deterministic handlers.
type AgentConfig struct {
	Name         string            `json:"name"`          // e.g., "supervisor", "coder", "reviewer"
	InputQueue   string            `json:"input_queue"`   // the queue this agent claims from
	OutputQueues map[string]string `json:"output_queues"` // queue_name -> description
	RoutingRules []Rule            `json:"routing_rules"` // for mocks: decision rules
	PromptURI    string            `json:"prompt_uri"`    // URI to prompt/system instruction (file: or doc:)
	ConfigURI    string            `json:"config_uri"`    // URI to agent config YAML (file: or doc:)
	IsMock       bool              `json:"is_mock"`       // if true, use mocked behavior
	Metadata     map[string]any    `json:"metadata"`      // arbitrary config
}

// GetOutputQueue returns the output queue for a given target.
// For supervisor: routes based on what it decides (coder, reviewer, etc.)
// For other agents: typically routes back to supervisor.
func (a *AgentConfig) GetOutputQueue(target string) string {
	if queue, ok := a.OutputQueues[target]; ok {
		return queue
	}
	return "" // no such queue
}

// ResolveURI resolves a URI to its content.
// Supports "file:" (filesystem) and "doc:" (entroq document) schemes.
func (a *AgentConfig) ResolveURI(ctx context.Context, client *entroq.EntroQ, uri string) ([]byte, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("invalid URI: %w", err)
	}

	switch u.Scheme {
	case "file", "":
		// Default to file if no scheme
		path := uri
		if u.Scheme == "file" {
			path = u.Path
		}
		return os.ReadFile(path)

	case "doc":
		// u.Opaque for "doc:sessions/abc" is "sessions/abc".
		// Split into namespace and key on the first slash.
		ns, key, ok := strings.Cut(u.Opaque, "/")
		if !ok {
			return nil, fmt.Errorf("doc URI %q: expected namespace/key form", uri)
		}
		docs, err := client.Docs(ctx, &entroq.DocQuery{
			Namespace: ns,
			KeyStart:  key,
			KeyEnd:    key + "\x00",
			Limit:     1,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to get doc %q: %w", u.Opaque, err)
		}
		if len(docs) == 0 {
			return nil, fmt.Errorf("doc %q not found", u.Opaque)
		}
		return docs[0].Content, nil

	default:
		return nil, fmt.Errorf("unsupported URI scheme: %s", u.Scheme)
	}
}

// WithName sets the agent name.
func (a *AgentConfig) WithName(name string) *AgentConfig {
	a.Name = name
	return a
}

// WithInputQueue sets the input queue.
func (a *AgentConfig) WithInputQueue(queue string) *AgentConfig {
	a.InputQueue = queue
	return a
}

// WithOutputQueue adds an output queue mapping.
func (a *AgentConfig) WithOutputQueue(target, queue string) *AgentConfig {
	if a.OutputQueues == nil {
		a.OutputQueues = make(map[string]string)
	}
	a.OutputQueues[target] = queue
	return a
}

// WithMock sets the IsMock flag.
func (a *AgentConfig) WithMock(isMock bool) *AgentConfig {
	a.IsMock = isMock
	return a
}

// WithMetadata sets a metadata key-value pair.
func (a *AgentConfig) WithMetadata(key string, value any) *AgentConfig {
	if a.Metadata == nil {
		a.Metadata = make(map[string]any)
	}
	a.Metadata[key] = value
	return a
}

// WithPromptURI sets the prompt URI.
func (a *AgentConfig) WithPromptURI(uri string) *AgentConfig {
	a.PromptURI = uri
	return a
}

// WithConfigURI sets the config URI.
func (a *AgentConfig) WithConfigURI(uri string) *AgentConfig {
	a.ConfigURI = uri
	return a
}

// SupervisorAgent is a convenience constructor for the supervisor mock.
func SupervisorAgent() *AgentConfig {
	return &AgentConfig{
		Name:       "supervisor",
		InputQueue: "supervisor",
		OutputQueues: map[string]string{
			"coder":      "coder_queue",
			"reviewer":   "reviewer_queue",
			"researcher": "researcher_queue",
		},
		RoutingRules: []Rule{
			// These would be loaded from config, but shown here as example:
			// {Condition: "contains:code", Action: "enqueue:coder", Queue: "coder_queue"},
			// {Condition: "contains:review", Action: "enqueue:reviewer", Queue: "reviewer_queue"},
		},
		IsMock: true,
		Metadata: map[string]any{
			"role": "orchestrator",
		},
	}
}

// CoderAgent is a convenience constructor for the coder mock.
func CoderAgent() *AgentConfig {
	return &AgentConfig{
		Name:       "coder",
		InputQueue: "coder_queue",
		OutputQueues: map[string]string{
			"supervisor": "supervisor",
		},
		IsMock: true,
		Metadata: map[string]any{
			"role": "code_analyzer",
		},
	}
}

// ReviewerAgent is a convenience constructor for the reviewer mock.
func ReviewerAgent() *AgentConfig {
	return &AgentConfig{
		Name:       "reviewer",
		InputQueue: "reviewer_queue",
		OutputQueues: map[string]string{
			"supervisor": "supervisor",
		},
		IsMock: true,
		Metadata: map[string]any{
			"role": "code_reviewer",
		},
	}
}

// ResearcherAgent is a convenience constructor for the researcher mock.
func ResearcherAgent() *AgentConfig {
	return &AgentConfig{
		Name:       "researcher",
		InputQueue: "researcher_queue",
		OutputQueues: map[string]string{
			"supervisor": "supervisor",
		},
		IsMock: true,
		Metadata: map[string]any{
			"role": "web_researcher",
		},
	}
}
