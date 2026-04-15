// Package config manages the agentq agents configuration file.
package config

import (
	"fmt"
	"os"

	"go.yaml.in/yaml/v3"
)

// Agent defines a specialist agent persona.
type Agent struct {
	Name        string `yaml:"name"`
	Queue       string `yaml:"queue"`
	Description string `yaml:"description"`
	PromptFile  string `yaml:"prompt_file,omitempty"`
	Cmd         string `yaml:"cmd,omitempty"`
}

// Config is the top-level structure of agents.yaml.
type Config struct {
	Agents []Agent `yaml:"agents"`
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

// Get returns the named agent and true, or zero value and false if not found.
func (c *Config) Get(name string) (Agent, bool) {
	for _, a := range c.Agents {
		if a.Name == name {
			return a, true
		}
	}
	return Agent{}, false
}
