package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/shiblon/agentq/pkg/llm"
	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/agentq/pkg/store"
	"github.com/shiblon/entroq"
)

// systemPrompt tells the LLM how to act as a routing supervisor.
const systemPrompt = `You are an orchestration supervisor for a multi-agent system.

Available agents:
- coder      : writes and edits code
- reviewer   : reviews code or written output for correctness and quality
- researcher : searches for information, investigates questions, summarizes findings

Your job: given the original user request and the work done so far, decide which
agent to invoke next -- or declare the task done.

Respond with exactly one JSON object and nothing else:
{"next": "<agent_name_or_done>", "reason": "<one brief sentence>"}

Valid values for "next": coder, reviewer, researcher, done.
`

// routeDecision is the JSON shape the LLM must produce.
type routeDecision struct {
	Next   string `json:"next"`
	Reason string `json:"reason"`
}

// Supervisor is the trampoline orchestrator. It accepts a task, consults the
// LLM to decide which specialist to invoke next, and enqueues that work.
type Supervisor struct {
	client *entroq.EntroQ
	store  *store.Store
	config *models.AgentConfig
	llm    llm.Client
}

// Option is a functional option for configuring a Supervisor.
type Option func(*Supervisor)

// New creates a new supervisor instance with functional options.
func New(client *entroq.EntroQ, opts ...Option) *Supervisor {
	s := &Supervisor{
		client: client,
		store:  store.New(client),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// WithConfig sets the agent configuration for the supervisor.
func WithConfig(fn func(*models.AgentConfig)) Option {
	return func(s *Supervisor) {
		cfg := &models.AgentConfig{}
		fn(cfg)
		s.config = cfg
	}
}

// WithLLM sets the LLM client used for routing decisions.
func WithLLM(c llm.Client) Option {
	return func(s *Supervisor) {
		s.llm = c
	}
}

// ProcessTask handles an incoming supervisor task.
// It loads the session, asks the LLM for the next step, then either
// enqueues a specialist task or marks the session complete.
func (s *Supervisor) ProcessTask(ctx context.Context, task *entroq.Task) ([]entroq.ModifyArg, error) {
	var appTask models.Task
	if err := json.Unmarshal(task.Value, &appTask); err != nil {
		return nil, fmt.Errorf("ProcessTask: unmarshal task: %w", err)
	}

	sessionID := appTask.SessionURI[len("doc:sessions/"):]

	var decision routeDecision
	if err := s.store.UpdateSession(ctx, sessionID, func(session *models.Session) error {
		var err error
		decision, err = s.decide(ctx, session)
		if err != nil {
			return err
		}

		artifact := models.NewArtifact(
			session.ID,
			"supervisor",
			"dispatch",
			fmt.Sprintf("next=%s reason=%s", decision.Next, decision.Reason),
		)
		session.Artifacts = append(session.Artifacts, *artifact)

		if decision.Next == "done" {
			session.Status = "completed"
		} else {
			session.Status = "in_progress"
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("ProcessTask: update session: %w", err)
	}

	mods := []entroq.ModifyArg{task.Delete()}

	if decision.Next != "done" {
		outputQueue := s.config.GetOutputQueue(decision.Next)
		if outputQueue == "" {
			return nil, fmt.Errorf("no output queue configured for agent: %s", decision.Next)
		}
		childTask := models.NewTask(outputQueue, appTask.SessionURI, map[string]any{
			"agent": decision.Next,
		})
		childBytes, err := json.Marshal(childTask)
		if err != nil {
			return nil, fmt.Errorf("marshal child task for %s: %w", decision.Next, err)
		}
		mods = append(mods, entroq.InsertingInto(outputQueue, entroq.WithRawValue(childBytes)))
	}

	return mods, nil
}

// decide asks the LLM (or falls back to keyword matching) which agent to invoke next.
func (s *Supervisor) decide(ctx context.Context, session *models.Session) (routeDecision, error) {
	if s.llm != nil {
		return s.decideWithLLM(ctx, session)
	}
	return s.decideWithKeywords(session), nil
}

// decideWithLLM builds a user prompt from the session and asks the LLM.
func (s *Supervisor) decideWithLLM(ctx context.Context, session *models.Session) (routeDecision, error) {
	userPrompt := buildUserPrompt(session)

	raw, err := s.llm.Complete(ctx, systemPrompt, userPrompt)
	if err != nil {
		return routeDecision{}, fmt.Errorf("llm routing: %w", err)
	}

	// The model sometimes wraps its JSON in markdown fences; strip them.
	text := strings.TrimSpace(raw)
	if i := strings.Index(text, "{"); i > 0 {
		text = text[i:]
	}
	if i := strings.LastIndex(text, "}"); i >= 0 {
		text = text[:i+1]
	}

	var d routeDecision
	if err := json.Unmarshal([]byte(text), &d); err != nil {
		return routeDecision{}, fmt.Errorf("parse llm response %q: %w", raw, err)
	}

	valid := map[string]bool{"coder": true, "reviewer": true, "researcher": true, "done": true}
	if !valid[d.Next] {
		return routeDecision{}, fmt.Errorf("llm returned unknown agent %q (raw: %s)", d.Next, raw)
	}

	return d, nil
}

// buildUserPrompt composes the context the LLM needs to make a routing decision.
func buildUserPrompt(session *models.Session) string {
	var b strings.Builder
	fmt.Fprintf(&b, "User request: %s\n\n", session.Prompt)

	if len(session.Artifacts) == 0 {
		b.WriteString("Work done so far: none.\n")
	} else {
		b.WriteString("Work done so far:\n")
		for _, a := range session.Artifacts {
			fmt.Fprintf(&b, "- [%s/%s] %s\n", a.AgentName, a.Type, a.Content)
		}
	}

	b.WriteString("\nWhat should happen next?")
	return b.String()
}

// decideWithKeywords is the fallback when no LLM is configured.
func (s *Supervisor) decideWithKeywords(session *models.Session) routeDecision {
	lower := strings.ToLower(session.Prompt)

	// If there are already artifacts from a specialist, we're done.
	for _, a := range session.Artifacts {
		if a.AgentName != "supervisor" {
			return routeDecision{Next: "done", Reason: "specialist has completed the work"}
		}
	}

	switch {
	case strings.Contains(lower, "research") || strings.Contains(lower, "investigate"):
		return routeDecision{Next: "researcher", Reason: "prompt requests research"}
	case strings.Contains(lower, "review") || strings.Contains(lower, "check"):
		return routeDecision{Next: "reviewer", Reason: "prompt requests a review"}
	default:
		return routeDecision{Next: "coder", Reason: "default: route to coder"}
	}
}
