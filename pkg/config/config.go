// Package config manages the agentq agents configuration file.
package config

import (
	"fmt"
	"os"

	"go.yaml.in/yaml/v3"
)

const (
	EnvWorkspaceRoot = "AGENTQ_WORKSPACE"
	EnvWorkspaceSelf = "AGENTQ_SELF"
)

// Agent defines a specialist agent persona.
type Agent struct {
	Name           string `yaml:"name"            json:"name"`
	Queue          string `yaml:"queue"           json:"queue"`
	Description    string `yaml:"description"     json:"description"`
	PromptFile     string `yaml:"prompt_file,omitempty"    json:"prompt_file,omitempty"`
	Cmd            string `yaml:"cmd,omitempty"            json:"cmd,omitempty"`
	// ApprovalSuffix is appended to Cmd when the task carries approved_actions.
	// For "claude --print" workers, set this to "--dangerously-skip-permissions".
	ApprovalSuffix string `yaml:"approval_suffix,omitempty" json:"approval_suffix,omitempty"`
}

// WorkspaceConfig describes the shared file workspace for agent workers.
// All fields can be overridden by environment variables:
//
//	AGENTQ_WORKSPACE -> Root
//	AGENTQ_SELF      -> Self
type WorkspaceConfig struct {
	// Root is the workspace root directory. Agents start here and repos are
	// cloned as subdirectories (e.g. github.com/owner/repo).
	Root string `yaml:"root,omitempty" json:"root,omitempty"`
	// Self is the path of the self-management repo relative to Root.
	// It contains system prompts (prompts/<agent>.txt) and this config file.
	Self string `yaml:"self,omitempty" json:"self,omitempty"`
	// SelfRemote is the remote URL used to clone the self repo if absent.
	// Derived from Self as https://<Self> if not set.
	SelfRemote string `yaml:"self_remote,omitempty" json:"self_remote,omitempty"`
}

// Config is the top-level structure of agents.yaml.
type Config struct {
	// Rubric is the approval policy text passed to the supervisor's system
	// prompt. It describes which agent actions can be auto-approved, which
	// require human review, and which are always rejected.
	Rubric    string          `yaml:"rubric,omitempty"    json:"rubric,omitempty"`
	Workspace WorkspaceConfig `yaml:"workspace,omitempty" json:"workspace,omitempty"`
	Agents    []Agent         `yaml:"agents"              json:"agents"`
}

// Load reads a Config from a YAML file. Returns an empty Config if the file
// does not exist (first run before any agents are added).
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}
	return &cfg, nil
}

// Save writes the Config to a YAML file, creating it if needed.
func (c *Config) Save(path string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write config %q: %w", path, err)
	}
	return nil
}

// Add appends an agent. Returns an error if the name is already taken.
func (c *Config) Add(a Agent) error {
	for _, existing := range c.Agents {
		if existing.Name == a.Name {
			return fmt.Errorf("agent %q already exists", a.Name)
		}
	}
	c.Agents = append(c.Agents, a)
	return nil
}

// Remove deletes an agent by name. Returns an error if not found.
func (c *Config) Remove(name string) error {
	for i, a := range c.Agents {
		if a.Name == name {
			c.Agents = append(c.Agents[:i], c.Agents[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("agent %q not found", name)
}

// ResolvedWorkspace returns the WorkspaceConfig with env var overrides applied.
// AGENTQ_WORKSPACE overrides Root; AGENTQ_SELF overrides Self.
func (c *Config) ResolvedWorkspace() WorkspaceConfig {
	w := c.Workspace
	if v := os.Getenv(EnvWorkspaceRoot); v != "" {
		w.Root = v
	}
	if v := os.Getenv(EnvWorkspaceSelf); v != "" {
		w.Self = v
	}
	return w
}

// Get returns the named agent and true, or zero value and false if not found.
func (c *Config) Get(name string) (Agent, bool) {
	for _, a := range c.Agents {
		if a.Name == name {
			return a, true
		}
	}
	return Agent{}, false
}
