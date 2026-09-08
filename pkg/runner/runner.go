// Package runner implements the runner microservice: an HTTP server that
// receives a task (JWT + transcript), configures an MCP connection, and
// invokes the baked-in agent command. The command is fixed at startup --
// the runner is not a general-purpose executor.
package runner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"

	"github.com/shiblon/agentq/pkg/models"
)

// Config holds the static configuration set at startup.
// Nothing here comes from the task payload.
type Config struct {
	// Command is the agent binary to invoke, e.g. "claude".
	Command string

	// Args are fixed arguments passed to Command before the runner's own
	// required flags (--mcp-config, --input-format, etc.),
	// e.g. []string{"--print"}.
	//
	// Args cannot grant the agent any capability: the containment flags in
	// run are appended afterwards, so they win on any conflict.
	Args []string

	// MCPAddr is the base URL of the MCP pool server,
	// e.g. "http://mcp-service:8081".
	MCPAddr string
}

// Runner is an HTTP microservice. POST / with a RunRequest body;
// it returns a RunResponse on success or a non-200 status on failure.
type Runner struct {
	cfg Config
}

// New creates a Runner from cfg.
func New(cfg Config) *Runner {
	return &Runner{cfg: cfg}
}

// Message is an alias for models.Message for backward compatibility within this package.
type Message = models.Message

// RunRequest is the JSON body the runner accepts.
type RunRequest struct {
	// JWT is the MCP session token. The runner passes it in the
	// X-AgentQ-Session-Config header of the MCP config; it is not inspected
	// or validated here.
	JWT string `json:"jwt"`

	// Messages is the conversation transcript assembled by the upstream
	// component (Supervisor / AgentQ). The runner serialises it to the
	// claude stream-json input format.
	Messages []Message `json:"messages"`

	// SystemPrompt, if non-empty, is passed as --system-prompt to the agent
	// command. Use this to inject the agent's persona and tool awareness.
	SystemPrompt string `json:"system_prompt,omitempty"`
}

// RunResponse is the JSON body returned on success.
type RunResponse struct {
	Output string `json:"output"`
}

func (r *Runner) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var task RunRequest
	if err := json.NewDecoder(req.Body).Decode(&task); err != nil {
		http.Error(w, fmt.Sprintf("decode request: %v", err), http.StatusBadRequest)
		return
	}
	if task.JWT == "" {
		http.Error(w, "missing jwt", http.StatusBadRequest)
		return
	}
	if len(task.Messages) == 0 {
		http.Error(w, "missing messages", http.StatusBadRequest)
		return
	}

	output, err := r.run(req.Context(), task)
	if err != nil {
		msg := fmt.Sprintf("run: %v", err)
		fmt.Fprintln(os.Stderr, "runner error:", msg)
		http.Error(w, msg, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(RunResponse{Output: output})
}

func (r *Runner) run(ctx context.Context, task RunRequest) (string, error) {
	cfgFile, err := r.writeMCPConfig(task.JWT)
	if err != nil {
		return "", fmt.Errorf("write mcp config: %w", err)
	}
	defer os.Remove(cfgFile)

	input, err := encodeMessages(task.Messages)
	if err != nil {
		return "", fmt.Errorf("encode messages: %w", err)
	}

	// Append runner-managed flags after caller-provided args.
	// --input-format stream-json + --output-format stream-json --verbose
	// are required for structured I/O with the claude CLI.
	// NOTE: --verbose is currently required by claude when combining
	// --print with --output-format stream-json. Revisit if that changes.
	args := make([]string, len(r.cfg.Args), len(r.cfg.Args)+14)
	copy(args, r.cfg.Args)
	if task.SystemPrompt != "" {
		args = append(args, "--system-prompt", task.SystemPrompt)
	}
	// Containment flags. The session JWT is the only thing permitted to grant
	// a capability, so the agent gets no built-in tools of its own, no MCP
	// server but ours, and no permission prompt it could block on with nobody
	// present to answer. These are appended last and are not configurable.
	args = append(args,
		"--tools", "",
		"--strict-mcp-config",
		"--permission-prompts", "none",
		"--mcp-config", cfgFile,
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
	)

	cmd := exec.CommandContext(ctx, r.cfg.Command, args...)
	cmd.Stdin = bytes.NewReader(input)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("agent exited: %w\nstderr: %s", err, stderr.String())
	}

	return parseResult(stdout.Bytes())
}

// encodeMessages serialises messages to claude's stream-json input format:
// one JSON object per line, {"type":"<role>","message":{"role":"<role>","content":[...]}}.
func encodeMessages(msgs []Message) ([]byte, error) {
	var buf bytes.Buffer
	for _, m := range msgs {
		blocks := make([]any, 0, len(m.Content))
		for _, b := range m.Content {
			switch b.Type {
			case "tool_use":
				blocks = append(blocks, map[string]any{
					"type":  "tool_use",
					"id":    b.ID,
					"name":  b.Name,
					"input": b.Input,
				})
			case "tool_result":
				blocks = append(blocks, map[string]any{
					"type":        "tool_result",
					"tool_use_id": b.ToolUseID,
					"content":     b.Text,
					"is_error":    b.IsError,
				})
			default: // "text" and anything else
				blocks = append(blocks, map[string]any{
					"type": "text",
					"text": b.Text,
				})
			}
		}
		line := map[string]any{
			"type": m.Role,
			"message": map[string]any{
				"role":    m.Role,
				"content": blocks,
			},
		}
		b, err := json.Marshal(line)
		if err != nil {
			return nil, err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

// parseResult scans stream-json output for the final result event and
// returns the response text. Returns an error if the result is absent or
// indicates failure.
func parseResult(output []byte) (string, error) {
	// Each line is a JSON object. We want {"type":"result","subtype":"success","result":"..."}.
	type resultEvent struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		Result  string `json:"result"`
		IsError bool   `json:"is_error"`
	}

	var last *resultEvent
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev resultEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.Type != "result" {
			continue
		}
		copy := ev
		last = &copy
	}
	if last == nil {
		return "", fmt.Errorf("no result event found in agent output")
	}
	if last.Subtype != "success" || last.IsError {
		return "", fmt.Errorf("agent returned error result (subtype=%q)", last.Subtype)
	}
	return last.Result, nil
}

// mcpConfigFile is the JSON structure written for --mcp-config.
//
// Uses Streamable HTTP transport ("http" type). The session JWT is passed in
// the X-AgentQ-Session-Config header -- it is configuration, not authentication,
// so Authorization: Bearer is deliberately avoided.
//
// NOTE: verify the exact header key name and "type" value against the claude CLI
// before shipping. `claude mcp add --transport http` works interactively; the
// JSON format for --mcp-config may differ slightly.
type mcpConfigFile struct {
	MCPServers map[string]mcpServerEntry `json:"mcpServers"`
}

type mcpServerEntry struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

func (r *Runner) writeMCPConfig(jwt string) (string, error) {
	cfg := mcpConfigFile{
		MCPServers: map[string]mcpServerEntry{
			"agentq": {
				Type: "http",
				URL:  r.cfg.MCPAddr + "/mcp",
				Headers: map[string]string{
					"X-AgentQ-Session-Config": jwt,
				},
			},
		},
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}

	f, err := os.CreateTemp("", "agentq-mcp-*.json")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	f.Close()

	return f.Name(), nil
}
