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
	"strings"
	"sync"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/shiblon/agentq/pkg/mcp"
	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/agentq/pkg/runner"
	"github.com/shiblon/agentq/pkg/sessionlog"
	"github.com/shiblon/entroq"
)

// Config holds the static configuration for an AgentQ worker instance.
// Tool ceiling enforcement and JWT minting happen here; the Supervisor only
// expresses intent (messages, workdir, optional bans) in the task payload.
type Config struct {
	// Name is the agent identifier, e.g. "coder".
	Name string

	// Description is the agent's role, injected as part of the system prompt.
	Description string

	// Grants is the ceiling of what this agent may do. A task may drop
	// grants but never add one. Grants with no scope root are rooted at the
	// task's workdir when the token is minted.
	Grants mcp.GrantSet

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

	// SystemPrompt, if non-empty, is used as the agent's system prompt instead
	// of the generated default. The effective tool list is appended so the LLM
	// knows what MCP tools are available. Populated from prompt_file in agents.yaml.
	SystemPrompt string
}

// Payload is the typed content of an AgentQ task's Payload field.
// The Supervisor fills this when creating tasks.
type Payload struct {
	// Messages is the conversation transcript to send to the agent.
	Messages []runner.Message `json:"messages"`

	// Workdir is the real filesystem path the agent's MCP session should expose.
	// Set by the Supervisor based on the session's working directory.
	Workdir string `json:"workdir"`

	// Tools, if non-empty, narrows this task to the named subset of the
	// agent's grants. Naming a tool the ceiling does not grant is refused
	// rather than ignored: attenuation only, never widening.
	Tools []string `json:"tools,omitempty"`

	// ParentSessionID is the plain session ID (not URI) of the supervisor session
	// that dispatched this task via dispatch_to_agent. Set by that tool; used by
	// the worker to write dispatch_complete to the parent's chunk log on completion.
	ParentSessionID string `json:"parent_session_id,omitempty"`

	// ChildSessionID is the ID generated at dispatch time identifying this
	// subtask in the parent session's pending set and chunk log.
	ChildSessionID string `json:"child_session_id,omitempty"`

	// Depth is the dispatch nesting level for this task. Threaded through from
	// the parent's JWT claim via dispatch_to_agent so the MCP server can enforce
	// MaxDispatchDepth on any further dispatches this agent attempts.
	Depth int `json:"depth,omitempty"`
}

// Worker claims tasks from an EntroQ inbox and dispatches them to the runner.
type Worker struct {
	cfg    Config
	eq     *entroq.EntroQ
	client *http.Client

	keyMu   sync.RWMutex
	privKey jwk.Key // guarded by keyMu; use getPrivKey / ReloadKey
}

// New creates a Worker from cfg.
func New(cfg Config, eq *entroq.EntroQ) *Worker {
	return &Worker{cfg: cfg, eq: eq, client: &http.Client{}, privKey: cfg.PrivKey}
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
//  2. Narrows Config.Grants by Payload.Tools, refusing any widening, and
//     roots any scopeless grant at the task's workdir.
//  3. Mints an MCP session JWT carrying those grants.
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

	grants, err := narrow(w.cfg.Grants, payload.Tools)
	if err != nil {
		return nil, fmt.Errorf("agentq %s: %w", w.cfg.Name, err)
	}
	grants = rootAt(grants, payload.Workdir)

	jwt, err := mcp.Mint(w.getPrivKey(), mcp.Claims{
		Issuer:    w.cfg.Issuer,
		SessionID: appTask.SessionURI,
		Grants:    grants,
		Depth:     payload.Depth,
		Expiry:    time.Now().Add(2 * time.Hour),
	})
	if err != nil {
		return nil, fmt.Errorf("agentq %s: mint jwt: %w", w.cfg.Name, err)
	}

	output, err := w.callRunner(ctx, runner.RunRequest{
		JWT:          jwt,
		Messages:     payload.Messages,
		SystemPrompt: w.systemPrompt(grants),
	})
	if err != nil {
		return nil, fmt.Errorf("agentq %s: runner: %w", w.cfg.Name, err)
	}

	// Use the task's ReplyTo if set (session-specific return address),
	// otherwise fall back to the worker's configured reply queue.
	replyQueue := appTask.ReplyTo
	if replyQueue == "" {
		replyQueue = w.cfg.ReplyQueue
	}
	resultTask := models.NewTask(replyQueue, appTask.SessionURI, map[string]any{
		"from_agent": w.cfg.Name,
		"output":     output,
	})

	args := []entroq.ModifyArg{
		entroq.InsertingInto(replyQueue, entroq.WithValue(resultTask)),
		task.Delete(),
	}

	// If this task was dispatched by a supervisor (parent/child IDs set), write
	// dispatch_complete to the parent's chunk log and clear the pending entry.
	if payload.ParentSessionID != "" && payload.ChildSessionID != "" {
		pendingDoc, err := sessionlog.FindPending(ctx, w.eq, payload.ChildSessionID)
		if err != nil {
			return nil, fmt.Errorf("agentq %s: find pending: %w", w.cfg.Name, err)
		}
		result := models.DispatchResult{
			AgentName: w.cfg.Name,
			SessionID: payload.ChildSessionID,
			Status:    models.DispatchCompleted,
			Summary:   output,
		}
		args = append(args,
			sessionlog.AppendArg(payload.ParentSessionID, sessionlog.Chunk{
				Type:    sessionlog.ChunkDispatchComplete,
				Agent:   w.cfg.Name,
				ChildID: payload.ChildSessionID,
				Result:  &result,
			}),
			entroq.DeletingDoc(pendingDoc),
		)
	}

	return args, nil
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

// systemPrompt returns the system prompt for this agent. If Config.SystemPrompt
// is set (loaded from prompt_file), it is used as the base and the granted
// tools are appended. Otherwise a generic identity prompt is generated.
func (w *Worker) systemPrompt(grants mcp.GrantSet) string {
	var base string
	if w.cfg.SystemPrompt != "" {
		base = w.cfg.SystemPrompt
	} else {
		var sb strings.Builder
		sb.WriteString("You are the ")
		sb.WriteString(w.cfg.Name)
		sb.WriteString(" agent.")
		if w.cfg.Description != "" {
			sb.WriteString(" Your job: ")
			sb.WriteString(w.cfg.Description)
		}
		base = sb.String()
	}
	tools := grants.Tools()
	if len(tools) > 0 {
		return base + "\n\nAvailable MCP tools: " + strings.Join(tools, ", ") + ".\nUse only these tools. Do not use any other tools."
	}
	return base + "\n\nYou have no MCP tools available. Respond using only your own knowledge."
}

// narrow returns ceiling restricted to grants for the named tools. An empty
// want leaves the ceiling untouched. Naming a tool the ceiling does not grant
// is an error rather than a silent drop, so a task asking for more than it may
// have fails at the only place authority is granted.
func narrow(ceiling mcp.GrantSet, want []string) (mcp.GrantSet, error) {
	if len(want) == 0 {
		return ceiling, nil
	}
	granted := ceiling.Tools()
	for _, name := range want {
		if !slices.Contains(granted, name) {
			return nil, fmt.Errorf("task asks for %q but this agent grants only %s", name, strings.Join(granted, ", "))
		}
	}
	out := make(mcp.GrantSet, 0, len(ceiling))
	for _, g := range ceiling {
		if slices.Contains(want, g.Tool) {
			out = append(out, g)
		}
	}
	return out, nil
}

// rootAt fills in the workdir for any grant that did not name a root of its
// own. Trusted mounts carry absolute roots and are left alone.
func rootAt(grants mcp.GrantSet, workdir string) mcp.GrantSet {
	out := make(mcp.GrantSet, len(grants))
	for i, g := range grants {
		if g.Scope.Root == "" {
			g.Scope.Root = workdir
		}
		out[i] = g
	}
	return out
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
