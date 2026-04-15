// Package exec provides a worker that delegates task processing to an
// external CLI via subprocess. The full session context is written to the
// command's stdin; stdout is captured as the result artifact.
package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	osexec "os/exec"

	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/agentq/pkg/store"
	"github.com/shiblon/entroq"
)

// Worker claims tasks from a queue, runs an external command for each one,
// and appends the command's stdout as an artifact to the session.
type Worker struct {
	name           string
	cmd            string // shell command, executed via sh -c
	promptFile     string // path to system prompt file (optional)
	replyQueue     string // queue to re-enqueue to after completion
	approvalSuffix string // appended to cmd when task carries approved_actions
	store          *store.Store
}

// Option is a functional option for Worker.
type Option func(*Worker)

// WithPromptFile sets the path to a system prompt file whose contents are
// prepended to the session context before being passed to the command.
func WithPromptFile(path string) Option {
	return func(w *Worker) { w.promptFile = path }
}

// WithReplyQueue overrides the queue the result task is posted to.
// Defaults to "supervisor".
func WithReplyQueue(q string) Option {
	return func(w *Worker) { w.replyQueue = q }
}

// WithApprovalSuffix sets a string appended to the shell command when the
// incoming task carries an approved_actions payload. For "claude --print"
// workers, set this to "--dangerously-skip-permissions".
func WithApprovalSuffix(s string) Option {
	return func(w *Worker) { w.approvalSuffix = s }
}

// New creates an exec Worker. cmd is a shell command string (run via sh -c).
func New(name, cmd string, eq *entroq.EntroQ, opts ...Option) *Worker {
	w := &Worker{
		name:       name,
		cmd:        cmd,
		replyQueue: "supervisor",
		store:      store.New(eq),
	}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

// ProcessTask loads the session, runs the configured command with session
// context on stdin, appends stdout as an artifact, and returns the
// modifications to apply atomically.
func (w *Worker) ProcessTask(ctx context.Context, task *entroq.Task) ([]entroq.ModifyArg, error) {
	var appTask models.Task
	if err := json.Unmarshal(task.Value, &appTask); err != nil {
		return nil, fmt.Errorf("exec %s: unmarshal task: %w", w.name, err)
	}

	sessionID := appTask.SessionURI[len("doc:sessions/"):]

	// If the task carries approved_actions and we have an approval suffix,
	// append it to the command so the subprocess runs with elevated permissions.
	cmd := w.cmd
	if w.approvalSuffix != "" && hasApproval(appTask.Payload) {
		cmd = w.cmd + " " + w.approvalSuffix
	}

	session, err := w.store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("exec %s: get session: %w", w.name, err)
	}

	systemPrompt, err := w.loadPrompt()
	if err != nil {
		return nil, fmt.Errorf("exec %s: %w", w.name, err)
	}

	input := buildInput(systemPrompt, session)

	output, err := w.runCmd(ctx, cmd, input)
	if err != nil {
		return nil, fmt.Errorf("exec %s: %w", w.name, err)
	}

	if err := w.store.UpdateSession(ctx, sessionID, func(s *models.Session) error {
		artifact := models.NewArtifact(s.ID, w.name, "result", output)
		s.Artifacts = append(s.Artifacts, *artifact)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("exec %s: update session: %w", w.name, err)
	}

	returnTask := models.NewTask(w.replyQueue, appTask.SessionURI, map[string]any{
		"from_agent": w.name,
	})
	returnBytes, err := json.Marshal(returnTask)
	if err != nil {
		return nil, fmt.Errorf("exec %s: marshal return task: %w", w.name, err)
	}

	return []entroq.ModifyArg{
		entroq.InsertingInto(w.replyQueue, entroq.WithRawValue(returnBytes)),
		task.Delete(),
	}, nil
}

func (w *Worker) loadPrompt() (string, error) {
	if w.promptFile == "" {
		return "", nil
	}
	data, err := os.ReadFile(w.promptFile)
	if err != nil {
		return "", fmt.Errorf("read prompt file %q: %w", w.promptFile, err)
	}
	return string(data), nil
}

func (w *Worker) runCmd(ctx context.Context, shellCmd, input string) (string, error) {
	cmd := osexec.CommandContext(ctx, "sh", "-c", shellCmd)
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("command %q failed: %w\nstderr: %s", w.cmd, err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

// hasApproval reports whether the task payload contains a non-empty
// approved_actions list, indicating the supervisor granted elevated permissions.
func hasApproval(payload map[string]any) bool {
	v, ok := payload["approved_actions"]
	if !ok || v == nil {
		return false
	}
	actions, ok := v.([]interface{})
	return ok && len(actions) > 0
}

// buildInput composes the full prompt passed to the subprocess on stdin.
func buildInput(systemPrompt string, session *models.Session) string {
	var b strings.Builder
	if systemPrompt != "" {
		b.WriteString(strings.TrimSpace(systemPrompt))
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "User request: %s\n\n", session.Prompt)
	if len(session.Artifacts) > 0 {
		b.WriteString("Work done so far:\n")
		for _, a := range session.Artifacts {
			fmt.Fprintf(&b, "- [%s/%s] %s\n", a.AgentName, a.Type, a.Content)
		}
		b.WriteString("\n")
	}
	b.WriteString("Perform your assigned task based on the above context.")
	return b.String()
}
