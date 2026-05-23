// Package supervisor implements the AgentQ supervisor worker.
// It claims tasks from a single inbox queue, handles both incoming user
// prompts and agent replies, and orchestrates leaf agents through the
// runner + dispatch_to_agent tool.
package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/shiblon/agentq/pkg/mcp"
	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/agentq/pkg/runner"
	"github.com/shiblon/agentq/pkg/sessionlog"
	"github.com/shiblon/agentq/pkg/store"
	"github.com/shiblon/entroq"
	"github.com/shiblon/entroq/pkg/worker"
)

// Config holds the static configuration for the supervisor worker.
type Config struct {
	// Issuer is the iss claim placed in minted JWTs.
	Issuer string

	// PrivKey is the RSA/EC key used to sign MCP session JWTs.
	PrivKey jwk.Key

	// MCPAddr is the base URL of the MCP pool server.
	// Must have dispatch_to_agent registered (i.e. started with EQ set).
	MCPAddr string

	// RunnerURL is the base URL of the runner microservice.
	RunnerURL string

	// Tools is the ceiling of MCP tools the supervisor may use.
	// Must include "dispatch_to_agent"; add file tools if the supervisor
	// needs to read context or write summaries.
	Tools []string

	// DefaultWorkdir is the filesystem path used when the session has no
	// workspace configured (Meta.WorkspaceRepo is empty).
	DefaultWorkdir string

	// SystemPrompt is prepended to every transcript as a system message.
	// If empty, DefaultSystemPrompt is used.
	SystemPrompt string

	// MaxDispatches is the maximum number of dispatch_to_agent calls allowed
	// across the lifetime of a session. When the limit is reached the supervisor
	// writes a hard-stop assistant message and returns without calling the runner.
	// Zero means unlimited.
	MaxDispatches int
}

// DefaultSystemPrompt is the base orchestrator prompt. BuildSystemPrompt appends the agent roster.
const DefaultSystemPrompt = `You are an AI orchestrator. Your job is to understand the user's request and delegate work to specialist agents using the dispatch_to_agent tool. Do not attempt to do the work yourself. When an agent completes, synthesize its output into a clear response for the user.`

// AgentInfo describes a known specialist agent for the system prompt.
type AgentInfo struct {
	Name        string
	Description string
}

// BuildSystemPrompt constructs the full system prompt from a base and an agent roster.
// If base is empty, DefaultSystemPrompt is used.
func BuildSystemPrompt(base string, agents []AgentInfo) string {
	if base == "" {
		base = DefaultSystemPrompt
	}
	if len(agents) == 0 {
		return base
	}
	var sb strings.Builder
	sb.WriteString(base)
	sb.WriteString("\n\nAvailable agents:\n")
	for _, a := range agents {
		sb.WriteString("  - ")
		sb.WriteString(a.Name)
		sb.WriteString(": ")
		sb.WriteString(a.Description)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// Worker claims tasks from the supervisor inbox and calls the runner.
// The same queue handles both user-initiated tasks and agent-reply tasks.
type Worker struct {
	cfg    Config
	store  *store.Store
	eq     *entroq.EntroQ
	client *http.Client

	keyMu   sync.RWMutex
	privKey jwk.Key
}

// New creates a Worker backed by eq.
func New(cfg Config, eq *entroq.EntroQ) *Worker {
	return &Worker{
		cfg:     cfg,
		store:   store.New(eq),
		eq:      eq,
		client:  &http.Client{},
		privKey: cfg.PrivKey,
	}
}

// ReloadKey atomically replaces the signing key. Safe to call from a signal
// handler goroutine while ProcessTask is running.
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

// ProcessTask is called by the eqworker framework for each claimed task.
// It writes any new user or dispatch-complete chunks to the session log,
// reads the full transcript from the log, calls the runner, appends the
// assistant response chunk, and posts the result to the reply queue.
func (w *Worker) ProcessTask(ctx context.Context, task *entroq.Task, appTask models.Task) ([]entroq.ModifyArg, error) {
	session, err := w.store.GetSessionByURI(ctx, appTask.SessionURI)
	if err != nil {
		return nil, worker.MoveErrorf("supervisor: load session %s: %v", appTask.SessionURI, err)
	}

	sessionID := strings.TrimPrefix(appTask.SessionURI, "doc:sessions/")

	if err := w.writeIncomingChunks(ctx, sessionID, appTask.Payload); err != nil {
		return nil, worker.MoveErrorf("supervisor: write chunks: %v", err)
	}

	if w.cfg.MaxDispatches > 0 {
		count, err := sessionlog.CountDispatches(ctx, w.eq, sessionID)
		if err != nil {
			return nil, worker.RetryErrorf("supervisor: count dispatches: %v", err)
		}
		if count >= w.cfg.MaxDispatches {
			stopMsg := fmt.Sprintf(
				"This session has reached its dispatch limit (%d agents invoked). No further agents will be dispatched. Please review the work so far and start a new session if more is needed.",
				w.cfg.MaxDispatches,
			)
			replyQueue := models.UserReplyQueue(session.ID)
			resultTask := models.NewTask(replyQueue, appTask.SessionURI, map[string]any{
				"from_agent": "supervisor",
				"output":     stopMsg,
			})
			log.Printf("supervisor: session %s hit dispatch limit (%d), stopping", sessionID, w.cfg.MaxDispatches)
			return []entroq.ModifyArg{
				entroq.InsertingInto(replyQueue, entroq.WithValue(resultTask)),
				task.Delete(),
				sessionlog.AppendArg(sessionID, sessionlog.Chunk{
					Type:    sessionlog.ChunkAssistantMessage,
					Content: stopMsg,
				}),
			}, nil
		}
	}

	transcript, err := w.buildTranscript(ctx, sessionID)
	if err != nil {
		return nil, worker.MoveErrorf("supervisor: build transcript: %v", err)
	}

	workdir := session.Meta.WorkspaceRepo
	if workdir == "" {
		workdir = w.cfg.DefaultWorkdir
	}

	supervisorQueue := task.Queue
	jwt, err := mcp.Mint(w.getPrivKey(), mcp.Claims{
		Issuer:        w.cfg.Issuer,
		SessionID:     appTask.SessionURI,
		Workdir:       workdir,
		ToolAllowlist: w.cfg.Tools,
		ReplyTo:       supervisorQueue,
		Expiry:        time.Now().Add(2 * time.Hour),
	})
	if err != nil {
		return nil, worker.MoveErrorf("supervisor: mint jwt: %v", err)
	}

	for i, m := range transcript {
		log.Printf("supervisor: transcript[%d] role=%s content=%.120s", i, m.Role, m.Content.TextOf())
	}

	output, err := w.callRunner(ctx, runner.RunRequest{
		JWT:      jwt,
		Messages: transcript,
	})
	if err != nil {
		return nil, worker.RetryErrorf("supervisor: runner: %v", err)
	}

	// Detect dispatch turns: if any child dispatches are pending, the runner
	// called dispatch_to_agent. Skip ChunkAssistantMessage — the tool_use/
	// tool_result pair already carries the context. Writing the "Dispatched!
	// fire-and-forget" text as an assistant chunk would appear after
	// ChunkDispatchPending in the log (due to timing), corrupting transcript order.
	pending, err := sessionlog.PendingList(ctx, w.eq, sessionID)
	if err != nil {
		return nil, worker.RetryErrorf("supervisor: check pending: %v", err)
	}

	replyQueue := models.UserReplyQueue(session.ID)
	resultTask := models.NewTask(replyQueue, appTask.SessionURI, map[string]any{
		"from_agent": "supervisor",
		"output":     output,
	})

	args := []entroq.ModifyArg{
		entroq.InsertingInto(replyQueue, entroq.WithValue(resultTask)),
		task.Delete(),
	}
	if len(pending) == 0 {
		// Synthesis turn: record the assistant response in the transcript.
		args = append(args, sessionlog.AppendArg(sessionID, sessionlog.Chunk{
			Type:    sessionlog.ChunkAssistantMessage,
			Content: output,
		}))
	}
	return args, nil
}

func (w *Worker) systemPrompt() string {
	if w.cfg.SystemPrompt != "" {
		return w.cfg.SystemPrompt
	}
	return DefaultSystemPrompt
}

// writeIncomingChunks writes user_message chunks to the session log based on
// the task payload type. Agent-reply payloads ("from_agent") are skipped
// because the agentq worker already wrote the dispatch_complete chunk.
func (w *Worker) writeIncomingChunks(ctx context.Context, sessionID string, payload map[string]any) error {
	if text, ok := payload["follow_up"].(string); ok {
		_, err := w.eq.Modify(ctx, sessionlog.AppendArg(sessionID, sessionlog.Chunk{
			Type:    sessionlog.ChunkUserMessage,
			Content: text,
		}))
		return err
	}

	if _, ok := payload["from_agent"]; ok {
		// dispatch_complete chunk already written by the agentq worker.
		return nil
	}

	if rawMessages, ok := payload["messages"]; ok {
		b, err := json.Marshal(rawMessages)
		if err != nil {
			return fmt.Errorf("encode messages: %w", err)
		}
		var messages []runner.Message
		if err := json.Unmarshal(b, &messages); err != nil {
			return fmt.Errorf("decode messages: %w", err)
		}
		args := make([]entroq.ModifyArg, 0, len(messages))
		for _, m := range messages {
			if m.Role == "user" {
				args = append(args, sessionlog.AppendArg(sessionID, sessionlog.Chunk{
					Type:    sessionlog.ChunkUserMessage,
					Content: m.Content.TextOf(),
				}))
			}
		}
		if len(args) > 0 {
			if _, err := w.eq.Modify(ctx, args...); err != nil {
				return fmt.Errorf("write user message chunks: %w", err)
			}
		}
		return nil
	}

	return fmt.Errorf("task payload has neither 'messages', 'follow_up', nor 'from_agent'")
}

// buildTranscript reads the full chunk log and returns the message slice for the LLM.
// The system prompt is prepended; raw tool mechanics are never included.
func (w *Worker) buildTranscript(ctx context.Context, sessionID string) ([]runner.Message, error) {
	msgs, err := sessionlog.Transcript(ctx, w.eq, sessionID)
	if err != nil {
		return nil, err
	}
	sys := models.TextMessage("system", w.systemPrompt())
	result := make([]runner.Message, 0, len(msgs)+1)
	result = append(result, sys)
	result = append(result, msgs...)
	return result, nil
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
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("runner returned %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}

	var result runner.RunResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	return result.Output, nil
}
