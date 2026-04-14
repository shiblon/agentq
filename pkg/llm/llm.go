// Package llm provides a thin client for LLM completions.
// The primary implementation targets Ollama's OpenAI-compatible endpoint.
// Other backends (direct OpenAI, Anthropic) can be added by implementing Client.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Client generates a completion from a system prompt and user prompt.
type Client interface {
	Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

// OllamaConfig holds configuration for the Ollama client.
type OllamaConfig struct {
	// BaseURL is the Ollama API base, e.g. "http://localhost:11434".
	BaseURL string
	// Model is the model to use, e.g. "qwen2.5:0.5b".
	Model string
	// HTTPClient allows injecting a custom HTTP client (nil uses http.DefaultClient).
	HTTPClient *http.Client
}

// OllamaClient calls Ollama's OpenAI-compatible /v1/chat/completions endpoint.
type OllamaClient struct {
	cfg  OllamaConfig
	http *http.Client
}

// NewOllama creates an OllamaClient with the given config.
// Defaults: BaseURL = "http://localhost:11434", Model = "qwen2.5:0.5b".
func NewOllamaClient(cfg OllamaConfig) *OllamaClient {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://localhost:11434"
	}
	if cfg.Model == "" {
		cfg.Model = "qwen2.5:0.5b"
	}
	h := cfg.HTTPClient
	if h == nil {
		h = http.DefaultClient
	}
	return &OllamaClient{cfg: cfg, http: h}
}

// chatRequest is the OpenAI-compatible request body.
type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatResponse is the minimal subset of the OpenAI response we need.
type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Complete sends a chat completion request and returns the assistant reply.
func (c *OllamaClient) Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	req := chatRequest{
		Model: c.cfg.Model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Stream: false,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("llm: marshal request: %w", err)
	}

	url := c.cfg.BaseURL + "/v1/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("llm: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("llm: http: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("llm: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("llm: status %d: %s", resp.StatusCode, raw)
	}

	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return "", fmt.Errorf("llm: unmarshal response: %w", err)
	}
	if cr.Error != nil {
		return "", fmt.Errorf("llm: api error: %s", cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return "", fmt.Errorf("llm: no choices in response")
	}

	return cr.Choices[0].Message.Content, nil
}
