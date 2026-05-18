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
	"net/http"
	"sync"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/shiblon/agentq/pkg/mcp"
	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/agentq/pkg/runner"
	"github.com/shiblon/agentq/pkg/store"
	"github.com/shiblon/entroq"
)

const transcriptArtifactType = "supervisor_transcript"

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
}

// Worker claims tasks from the supervisor inbox and calls the runner.
// The same queue handles both user-initiated tasks and agent-reply tasks.
type Worker struct {
	cfg    Config
	store  *store.Store
	client *http.Client

	keyMu   sync.RWMutex
	privKey jwk.Key
}

// New creates a Worker backed by eq.
func New(cfg Config, eq *entroq.EntroQ) *Worker {
	return &Worker{
		cfg:     cfg,
		store:   store.New(eq),
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
// It dispatches to the runner and posts the supervisor's response to the
// session's UserReplyTo queue.
func (w *Worker) ProcessTask(ctx context.Context, task *entroq.Task, appTask models.Task) ([]entroq.ModifyArg, error) {
	session, err := w.store.GetSessionByURI(ctx, appTask.SessionURI)
	if err != nil {
		return nil, fmt.Errorf("supervisor: load session %s: %w", appTask.SessionURI, err)
	}

	transcript, err := w.buildTranscript(session, appTask.Payload)
	if err != nil {
		return nil, fmt.Errorf("supervisor: build transcript: %w", err)
	}

	workdir := session.Meta.WorkspaceRepo
	if workdir == "" {
		workdir = w.cfg.DefaultWorkdir
	}

	jwt, err := mcp.Mint(w.getPrivKey(), mcp.Claims{
		Issuer:        w.cfg.Issuer,
		SessionID:     appTask.SessionURI,
		Workdir:       workdir,
		ToolAllowlist: w.cfg.Tools,
		Expiry:        time.Now().Add(2 * time.Hour),
	})
	if err != nil {
		return nil, fmt.Errorf("supervisor: mint jwt: %w", err)
	}

	output, err := w.callRunner(ctx, runner.RunRequest{
		JWT:      jwt,
		Messages: transcript,
	})
	if err != nil {
		return nil, fmt.Errorf("supervisor: runner: %w", err)
	}

	// Persist transcript + assistant response for the next round.
	updated := append(transcript, runner.Message{Role: "assistant", Content: output})
	if err := w.saveTranscript(ctx, session, updated); err != nil {
		return nil, fmt.Errorf("supervisor: save transcript: %w", err)
	}

	replyQueue := models.UserReplyQueue(session.ID)
	resultTask := models.NewTask(replyQueue, appTask.SessionURI, map[string]any{
		"from_agent": "supervisor",
		"output":     output,
	})

	return []entroq.ModifyArg{
		entroq.InsertingInto(replyQueue, entroq.WithValue(resultTask)),
		task.Delete(),
	}, nil
}

// buildTranscript constructs the message slice to pass to the runner.
//
// Two cases:
//   - "messages" in payload: user-initiated turn; use those messages directly.
//   - "from_agent" in payload: leaf agent reply; load saved transcript and
//     append the agent result as a user message.
func (w *Worker) buildTranscript(session *models.Session, payload map[string]any) ([]runner.Message, error) {
	if agentNameRaw, ok := payload["from_agent"]; ok {
		agentName, _ := agentNameRaw.(string)
		output, _ := payload["output"].(string)

		transcript, err := w.loadTranscript(session)
		if err != nil {
			return nil, err
		}
		transcript = append(transcript, runner.Message{
			Role:    "user",
			Content: fmt.Sprintf("[Agent %s completed]\n%s", agentName, output),
			Agent:   agentName,
		})
		return transcript, nil
	}

	if rawMessages, ok := payload["messages"]; ok {
		b, err := json.Marshal(rawMessages)
		if err != nil {
			return nil, fmt.Errorf("encode messages: %w", err)
		}
		var messages []runner.Message
		if err := json.Unmarshal(b, &messages); err != nil {
			return nil, fmt.Errorf("decode messages: %w", err)
		}
		return messages, nil
	}

	return nil, fmt.Errorf("task payload has neither 'messages' nor 'from_agent'")
}

// loadTranscript returns the most recent supervisor_transcript artifact from
// the session, or nil if no transcript has been saved yet (fresh session).
func (w *Worker) loadTranscript(session *models.Session) ([]runner.Message, error) {
	var latest *models.Artifact
	for i := range session.Artifacts {
		a := &session.Artifacts[i]
		if a.Type != transcriptArtifactType {
			continue
		}
		if latest == nil || a.CreatedAt.After(latest.CreatedAt) {
			latest = a
		}
	}
	if latest == nil {
		return nil, nil
	}
	var msgs []runner.Message
	if err := json.Unmarshal([]byte(latest.Content), &msgs); err != nil {
		return nil, fmt.Errorf("decode transcript artifact: %w", err)
	}
	return msgs, nil
}

// saveTranscript appends a new supervisor_transcript artifact and persists the session.
func (w *Worker) saveTranscript(ctx context.Context, session *models.Session, transcript []runner.Message) error {
	content, err := json.Marshal(transcript)
	if err != nil {
		return fmt.Errorf("marshal transcript: %w", err)
	}
	artifact := models.NewArtifact(session.ID, "supervisor", transcriptArtifactType, string(content))
	return w.store.UpdateSession(ctx, session.ID, func(s *models.Session) error {
		s.Artifacts = append(s.Artifacts, *artifact)
		return nil
	})
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
