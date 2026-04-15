package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/shiblon/agentq/pkg/config"
	"github.com/shiblon/agentq/pkg/llm"
	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/agentq/pkg/store"
	"github.com/shiblon/entroq"
)

// buildSystemPrompt constructs the routing prompt from the configured agent list.
func buildSystemPrompt(agents []config.Agent) string {
	var b strings.Builder
	b.WriteString("You are a routing supervisor. Output ONLY a single JSON object.\n\n")
	b.WriteString("Available agents:\n")
	for _, a := range agents {
		fmt.Fprintf(&b, "- %s: %s\n", a.Name, a.Description)
	}
	b.WriteString(`
Routing rules:
- No prior work exists -> dispatch the appropriate specialist for the request.
- Only use a reviewing/checking agent after a producing agent has already run.
- Once a specialist has completed the task, use "done".

Example output (adapt to the actual situation):
{"next": "done", "reason": "coder has produced the requested function"}

Your response must be a JSON object with string fields "next" and "reason". Nothing else.
`)
	return b.String()
}

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
	agents []config.Agent
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

// WithAgents sets the known specialist agents for dynamic prompt building
// and route validation. Also populates the output queue map on the config.
func WithAgents(agents []config.Agent) Option {
	return func(s *Supervisor) {
		s.agents = agents
		if s.config == nil {
			s.config = &models.AgentConfig{}
		}
		if s.config.OutputQueues == nil {
			s.config.OutputQueues = make(map[string]string)
		}
		for _, a := range agents {
			s.config.OutputQueues[a.Name] = a.Queue
		}
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

	raw, err := s.llm.Complete(ctx, buildSystemPrompt(s.agents), userPrompt)
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
		// Small models sometimes echo the context instead of emitting JSON.
		// If a specialist has already produced output, the safest fallback is done.
		for _, a := range session.Artifacts {
			if a.AgentName != "supervisor" {
				return routeDecision{Next: "done", Reason: "llm parse failed; specialist work exists"}, nil
			}
		}
		return routeDecision{}, fmt.Errorf("parse llm response %q: %w", raw, err)
	}

	valid := map[string]bool{"done": true}
	for _, a := range s.agents {
		valid[a.Name] = true
	}
	if !valid[d.Next] {
		return routeDecision{}, fmt.Errorf("llm returned unknown agent %q (raw: %s)", d.Next, raw)
	}

	// Rule guard: small models sometimes skip the "no prior work" rule.
	// Only count fresh (non-inherited) artifacts -- inherited artifacts are
	// context from a prior session, not work completed in this one.
	hasFreshWork := false
	for _, a := range session.Artifacts {
		if a.AgentName != "supervisor" && a.OriginSessionID == "" {
			hasFreshWork = true
			break
		}
	}

	// If no specialist has run yet in this session, "done" and reviewer-type
	// agents are invalid first choices.
	if !hasFreshWork && (d.Next == "done" || s.looksLikeReviewer(d.Next)) {
		kw := s.decideWithKeywords(session)
		return routeDecision{Next: kw.Next, Reason: "llm rule violation corrected: " + kw.Reason}, nil
	}

	return d, nil
}

// buildUserPrompt composes the context the LLM needs to make a routing decision.
func buildUserPrompt(session *models.Session) string {
	var b strings.Builder
	fmt.Fprintf(&b, "User request: %s\n\n", session.Prompt)

	var inherited, fresh []models.Artifact
	for _, a := range session.Artifacts {
		if a.AgentName == "supervisor" {
			continue
		}
		if a.OriginSessionID != "" {
			inherited = append(inherited, a)
		} else {
			fresh = append(fresh, a)
		}
	}

	if len(inherited) == 0 && len(fresh) == 0 {
		b.WriteString("Work done so far: none.\n")
	} else {
		if len(inherited) > 0 {
			b.WriteString("Prior session context (inherited):\n")
			for _, a := range inherited {
				fmt.Fprintf(&b, "- [%s/%s from session %s] %s\n",
					a.AgentName, a.Type, a.OriginSessionID, a.Content)
			}
			b.WriteString("\n")
		}
		if len(fresh) > 0 {
			b.WriteString("Work done in this session:\n")
			for _, a := range fresh {
				fmt.Fprintf(&b, "- [%s/%s] %s\n", a.AgentName, a.Type, a.Content)
			}
		}
	}

	b.WriteString("\nWhat should happen next?")
	return b.String()
}

// looksLikeReviewer returns true if the named agent's description suggests it
// should only run after another specialist (contains "review" or "check").
func (s *Supervisor) looksLikeReviewer(name string) bool {
	for _, a := range s.agents {
		if a.Name == name {
			desc := strings.ToLower(a.Description)
			return strings.Contains(desc, "review") || strings.Contains(desc, "check")
		}
	}
	return false
}

// decideWithKeywords is the fallback when no LLM is configured.
// Matches the prompt against agent descriptions; falls back to the first
// non-reviewer agent if nothing matches.
func (s *Supervisor) decideWithKeywords(session *models.Session) routeDecision {
	// If a specialist has already done fresh work in this session, we're done.
	for _, a := range session.Artifacts {
		if a.AgentName != "supervisor" && a.OriginSessionID == "" {
			return routeDecision{Next: "done", Reason: "specialist has completed the work"}
		}
	}

	lower := strings.ToLower(session.Prompt)

	// Try to match a non-reviewer agent by description keywords.
	for _, a := range s.agents {
		if s.looksLikeReviewer(a.Name) {
			continue
		}
		for _, word := range strings.Fields(strings.ToLower(a.Description)) {
			if len(word) >= 4 && strings.Contains(lower, word) {
				return routeDecision{Next: a.Name, Reason: "keyword match on description: " + word}
			}
		}
	}

	// Default to the first non-reviewer agent.
	for _, a := range s.agents {
		if !s.looksLikeReviewer(a.Name) {
			return routeDecision{Next: a.Name, Reason: "default: first available specialist"}
		}
	}

	return routeDecision{Next: "done", Reason: "no agents configured"}
}
