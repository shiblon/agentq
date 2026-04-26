// Package exec provides a worker that delegates task processing to an
// external CLI via subprocess. The full session context is written to the
// command's stdin; stdout is captured as the result artifact.
package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	osexec "os/exec"

	"github.com/shiblon/agentq/pkg/approval"
	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/agentq/pkg/store"
	"github.com/shiblon/agentq/pkg/workspace"
	"github.com/shiblon/entroq"
)

// Worker claims tasks from a queue, runs an external command for each one,
// and appends the command's stdout as an artifact to the session.
type Worker struct {
	name           string
	cmd            string // shell command, executed via sh -c
	promptFile     string // path to system prompt file (optional); overridden by workspace
	replyQueue     string // queue to re-enqueue to after completion
	approvalSuffix string // appended to cmd when task carries approved_actions
	verifier       *approval.Verifier   // if set, approval_token must verify before elevation
	ws             *workspace.Workspace // if set, manages working dir and prompt loading
	commitWork     bool                 // if true, commit + push after each task
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

// WithWorkspace attaches a Workspace to the worker. Before each task the
// worker pulls the self repo and loads the agent's system prompt from it.
// The working directory is set to the session's target repo (from session
// metadata key "workspace_repo"), cloning it first if absent.
func WithWorkspace(ws *workspace.Workspace) Option {
	return func(w *Worker) { w.ws = ws }
}

// WithVerifier sets an approval token verifier. When set, the worker checks the
// approval_token payload field before appending the approval suffix. Without a
// valid Macaroon the command runs without elevated permissions, regardless of
// what approved_actions claims.
func WithVerifier(v *approval.Verifier) Option {
	return func(w *Worker) { w.verifier = v }
}

// WithCommitWork instructs the worker to git-commit and push any changes in
// the target repo after each task completes. Requires WithWorkspace.
func WithCommitWork(commit bool) Option {
	return func(w *Worker) { w.commitWork = commit }
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

	// Append the approval suffix only when the task carries approved_actions AND
	// the approval token verifies (if a verifier is configured). Without a valid
	// Macaroon the subprocess runs without elevated permissions.
	cmd := w.cmd
	if w.approvalSuffix != "" && hasApproval(appTask.Payload) && w.approvalVerified(appTask, sessionID) {
		cmd = w.cmd + " " + w.approvalSuffix
	}

	session, err := w.store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("exec %s: get session: %w", w.name, err)
	}

	// Workspace setup: pull self repo, resolve prompt, prepare target repo.
	workDir := ""
	if w.ws != nil {
		if err := w.ws.PullSelf(ctx, ""); err != nil {
			// Non-fatal: log and continue with potentially stale prompts.
			log.Printf("exec %s: pull self repo: %v", w.name, err)
		}
		if repoPath := session.Meta.WorkspaceRepo; repoPath != "" {
			dir, err := w.ws.PrepareRepo(ctx, repoPath, "")
			if err != nil {
				return nil, fmt.Errorf("exec %s: prepare repo %q: %w", w.name, repoPath, err)
			}
			workDir = dir
		}
	}

	systemPrompt, err := w.loadPrompt(ctx)
	if err != nil {
		return nil, fmt.Errorf("exec %s: %w", w.name, err)
	}

	input := buildInput(systemPrompt, session)

	agentToken, _ := appTask.Payload["agent_token"].(string)
	output, err := w.runCmd(ctx, cmd, workDir, agentToken, input)
	if err != nil {
		return nil, fmt.Errorf("exec %s: %w", w.name, err)
	}

	// Optionally commit work in the target repo after a successful run.
	if w.ws != nil && w.commitWork && workDir != "" {
		msg := fmt.Sprintf("agentq: session %s agent %s", sessionID, w.name)
		if err := w.ws.CommitWork(ctx, workDir, msg); err != nil {
			log.Printf("exec %s: commit work: %v", w.name, err)
		}
	}

	if err := w.store.UpdateSession(ctx, sessionID, func(s *models.Session) error {
		artifact := models.NewArtifact(s.ID, w.name, "result", output)
		s.Artifacts = append(s.Artifacts, *artifact)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("exec %s: update session: %w", w.name, err)
	}

	returnPayload := map[string]any{"from_agent": w.name}
	if pt, _ := appTask.Payload["provenance_token"].(string); pt != "" {
		returnPayload["provenance_token"] = pt
	}
	if dr, _ := appTask.Payload["dispatch_receipt"].(string); dr != "" {
		returnPayload["dispatch_receipt"] = dr
	}
	returnTask := models.NewTask(w.replyQueue, appTask.SessionURI, returnPayload)
	returnBytes, err := json.Marshal(returnTask)
	if err != nil {
		return nil, fmt.Errorf("exec %s: marshal return task: %w", w.name, err)
	}

	return []entroq.ModifyArg{
		entroq.InsertingInto(w.replyQueue, entroq.WithRawValue(returnBytes)),
		task.Delete(),
	}, nil
}

// loadPrompt returns the system prompt. Workspace takes precedence over
// promptFile: if a workspace is configured, the prompt is read from the self
// repo. Falls back to promptFile, then empty string.
func (w *Worker) loadPrompt(ctx context.Context) (string, error) {
	if w.ws != nil {
		p, err := w.ws.PromptFor(w.name)
		if err != nil {
			return "", fmt.Errorf("workspace prompt for %q: %w", w.name, err)
		}
		if p != "" {
			return p, nil
		}
		// Fall through to promptFile if workspace has no prompt for this agent.
	}
	if w.promptFile == "" {
		return "", nil
	}
	data, err := os.ReadFile(w.promptFile)
	if err != nil {
		return "", fmt.Errorf("read prompt file %q: %w", w.promptFile, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// runCmd runs shellCmd via sh -c with input on stdin. workDir sets the working
// directory; empty string means inherit the process working directory.
// agentToken, if non-empty, is passed as AGENTQ_TOKEN in the subprocess
// environment so the agent can authenticate back to the agentq API.
func (w *Worker) runCmd(ctx context.Context, shellCmd, workDir, agentToken, input string) (string, error) {
	cmd := osexec.CommandContext(ctx, "sh", "-c", shellCmd)
	cmd.Stdin = strings.NewReader(input)
	if workDir != "" {
		cmd.Dir = workDir
	}
	cmd.Env = envWithToken(agentToken)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("command %q failed: %w\nstderr: %s", w.cmd, err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

// envWithToken returns os.Environ() with AGENTQ_TOKEN set if token is non-empty.
func envWithToken(token string) []string {
	env := os.Environ()
	if token == "" {
		return env
	}
	// Replace any existing AGENTQ_TOKEN entry.
	result := make([]string, 0, len(env)+1)
	for _, e := range env {
		if !strings.HasPrefix(e, "AGENTQ_TOKEN=") {
			result = append(result, e)
		}
	}
	return append(result, "AGENTQ_TOKEN="+token)
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

// approvalVerified returns true when the task's approval_token is valid for the
// given session. When no verifier is configured it falls back to trusting the
// approved_actions field directly (backward-compatible dev mode).
func (w *Worker) approvalVerified(task models.Task, sessionID string) bool {
	if w.verifier == nil {
		return true // no verifier configured: trust the payload (dev mode)
	}
	tok, _ := task.Payload["approval_token"].(string)
	if tok == "" {
		log.Printf("%s: approval suffix requested but no approval_token in payload -- running without elevation", w.name)
		return false
	}
	if _, err := w.verifier.Verify(tok, sessionID); err != nil {
		log.Printf("%s: approval token verification failed: %v -- running without elevation", w.name, err)
		return false
	}
	return true
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
