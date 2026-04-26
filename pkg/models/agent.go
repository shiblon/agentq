package models

// AgentConfig defines an agent's runtime identity: the queue it claims from
// and the queues it can dispatch work to.
type AgentConfig struct {
	Name         string            `json:"name"`
	InputQueue   string            `json:"input_queue"`
	OutputQueues map[string]string `json:"output_queues"`
}

// GetOutputQueue returns the queue name for the given routing target,
// or empty string if the target is not registered.
func (a *AgentConfig) GetOutputQueue(target string) string {
	return a.OutputQueues[target]
}
