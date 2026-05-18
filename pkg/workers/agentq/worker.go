// Package agentq implements the AgentQ worker: it claims tasks from an EntroQ
// inbox, mints a per-session MCP config JWT, forwards the task to the runner
// microservice via HTTP, and posts the result back to the reply queue.
package agentq

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/shiblon/agentq/pkg/mcp"
	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/agentq/pkg/runner"
	"github.com/shiblon/entroq"
)

// Config holds the static configuration for an AgentQ worker instance.
// Tool ceiling enforcement and JWT minting happen here; the Supervisor only
// expresses intent (messages, workdir, optional bans) in the task payload.
type Config struct {
	// Name is the agent identifier, e.g. "coder".
	Name string

	// Tools is the ceiling of MCP tools this agent may use, already expanded
	// (no wildcards). Use ExpandTools to resolve ["*"] before constructing Config.
	Tools []string

	// PrivKey is the RSA private key used to sign MCP session JWTs.
	PrivKey jwk.Key

	// Issuer is the iss claim placed in minted JWTs, e.g. "agentq".
	Issuer string

	// MCPAddr is the base URL of the MCP pool server, e.g. "http://mcp:8081".
	MCPAddr string

	// RunnerURL is the base URL of the runner microservice, e.g. "http://runner:8082".
	RunnerURL string

	// ReplyQueue is the EntroQ queue to post results to.
	ReplyQueue string
}

// Payload is the typed content of an AgentQ task's Payload field.
// The Supervisor fills this when creating tasks.
type Payload struct {
	// Messages is the conversation transcript to send to the agent.
	Messages []runner.Message `json:"messages"`

	// Workdir is the real filesystem path the agent's MCP session should expose.
	// Set by the Supervisor based on the session's working directory.
	Workdir string `json:"workdir"`

	// AllowedTools, if non-empty, narrows the agent's configured ceiling to
	// only the tools listed here (intersection). Applied before BlockedTools.
	AllowedTools []string `json:"allowed_tools,omitempty"`

	// BlockedTools removes specific tools from the effective set after
	// AllowedTools has been applied. Absent means no blocks.
	BlockedTools []string `json:"blocked_tools,omitempty"`
}

// Worker claims tasks from an EntroQ inbox and dispatches them to the runner.
type Worker struct {
	cfg    Config
	client *http.Client

	keyMu   sync.RWMutex
	privKey jwk.Key // guarded by keyMu; use getPrivKey / ReloadKey
}

// New creates a Worker from cfg.
func New(cfg Config) *Worker {
	return &Worker{cfg: cfg, client: &http.Client{}, privKey: cfg.PrivKey}
}

// ReloadKey atomically replaces the signing key. Safe to call from a signal
// handler goroutine while ProcessTask is running. Intended for SIGHUP-triggered
// key rotation when using --key-file with Vault Agent or similar.
func (w *Worker) ReloadKey(key jwk.Key) {
	w.keyMu.Lock()
	w.privKey = key
	w.keyMu.Unlock()
}

func (w *Worker) getPrivKey() jwk.Key {
	w.keyMu.RLock()
	defer w.keyMu.RUnlock()
	return w.privKey
}

// ProcessTask is called by the task loop for each claimed task. The entroq
// worker framework pre-unmarshals task.Value into appTask before calling here.
// It:
//  1. Extracts the typed Payload from appTask.
//  2. Computes effective tools: Config.Tools minus Payload.BlockedTools.
//  3. Mints an MCP session JWT with those tools and the workdir.
//  4. Calls the runner via HTTP.
//  5. Returns EntroQ modifications: insert result task + delete claimed task.
func (w *Worker) ProcessTask(ctx context.Context, task *entroq.Task, appTask models.Task) ([]entroq.ModifyArg, error) {
	var payload Payload
	if err := remarshal(appTask.Payload, &payload); err != nil {
		return nil, fmt.Errorf("agentq %s: unmarshal payload: %w", w.cfg.Name, err)
	}
	if len(payload.Messages) == 0 {
		return nil, fmt.Errorf("agentq %s: task missing messages", w.cfg.Name)
	}

	effectiveTools := applyBlocks(applyAllowed(w.cfg.Tools, payload.AllowedTools), payload.BlockedTools)

	jwt, err := mcp.Mint(w.getPrivKey(), mcp.Claims{
		Issuer:        w.cfg.Issuer,
		SessionID:     appTask.SessionURI,
		Workdir:       payload.Workdir,
		ToolAllowlist: effectiveTools,
		Expiry:        time.Now().Add(2 * time.Hour),
	})
	if err != nil {
		return nil, fmt.Errorf("agentq %s: mint jwt: %w", w.cfg.Name, err)
	}

	output, err := w.callRunner(ctx, runner.RunRequest{
		JWT:      jwt,
		Messages: payload.Messages,
	})
	if err != nil {
		return nil, fmt.Errorf("agentq %s: runner: %w", w.cfg.Name, err)
	}

	resultTask := models.NewTask(w.cfg.ReplyQueue, appTask.SessionURI, map[string]any{
		"from_agent": w.cfg.Name,
		"output":     output,
	})
	resultBytes, err := json.Marshal(resultTask)
	if err != nil {
		return nil, fmt.Errorf("agentq %s: marshal result: %w", w.cfg.Name, err)
	}

	return []entroq.ModifyArg{
		entroq.InsertingInto(w.cfg.ReplyQueue, entroq.WithRawValue(resultBytes)),
		task.Delete(),
	}, nil
}

func (w *Worker) callRunner(ctx context.Context, req runner.RunRequest) (string, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, w.cfg.RunnerURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := w.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("POST %s: %w", w.cfg.RunnerURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("runner returned %d", resp.StatusCode)
	}

	var result runner.RunResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	return result.Output, nil
}

// applyAllowed narrows tools to the intersection with allowed.
// If allowed is empty, the full tools list is returned unchanged.
func applyAllowed(tools, allowed []string) []string {
	if len(allowed) == 0 {
		return tools
	}
	result := make([]string, 0, len(allowed))
	for _, t := range tools {
		if slices.Contains(allowed, t) {
			result = append(result, t)
		}
	}
	return result
}

// applyBlocks returns tools with any blocked names removed.
func applyBlocks(tools, blocked []string) []string {
	if len(blocked) == 0 {
		return tools
	}
	result := make([]string, 0, len(tools))
	for _, t := range tools {
		if !slices.Contains(blocked, t) {
			result = append(result, t)
		}
	}
	return result
}

// ExpandTools resolves the ["*"] wildcard to the full set of MCP file tool
// names. Call this when building a Config from agents.yaml before passing
// Tools to the worker. An empty slice is returned as-is (fail-closed).
func ExpandTools(tools []string) []string {
	for _, t := range tools {
		if t == "*" {
			all := mcp.AllFileTools()
			names := make([]string, len(all))
			for i, tool := range all {
				names[i] = tool.Tool.Name
			}
			return names
		}
	}
	return tools
}

// remarshal round-trips v through JSON to populate dst. Used to convert a
// map[string]any payload field into a typed struct without defining a custom
// unmarshaler on models.Task.
func remarshal(v any, dst any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}
