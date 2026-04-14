package models

// AgentConfig defines an agent's behavior and queues.
// For real agents: just queue names and basic metadata.
// For mocks: also include routing rules and deterministic handlers.
type AgentConfig struct {
	Name          string                `json:"name"`         // e.g., "supervisor", "coder", "reviewer"
	InputQueue    string                `json:"input_queue"`  // the queue this agent claims from
	OutputQueues  map[string]string     `json:"output_queues"` // queue_name -> description
	RoutingRules  []Rule                `json:"routing_rules"` // for mocks: decision rules
	PromptFile    string                `json:"prompt_file"`   // path to prompt/system instruction
	ConfigFile    string                `json:"config_file"`   // path to agent config YAML
	IsMock        bool                  `json:"is_mock"`       // if true, use mocked behavior
	Metadata      map[string]any        `json:"metadata"`      // arbitrary config
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

// SupervisorAgent is a convenience constructor for the supervisor mock.
func SupervisorAgent() *AgentConfig {
	return &AgentConfig{
		Name:         "supervisor",
		InputQueue:   "supervisor",
		OutputQueues: map[string]string{
			"coder":     "coder_queue",
			"reviewer":  "reviewer_queue",
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
		Name:         "coder",
		InputQueue:   "coder_queue",
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
		Name:         "reviewer",
		InputQueue:   "reviewer_queue",
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
		Name:         "researcher",
		InputQueue:   "researcher_queue",
		OutputQueues: map[string]string{
			"supervisor": "supervisor",
		},
		IsMock: true,
		Metadata: map[string]any{
			"role": "web_researcher",
		},
	}
}
