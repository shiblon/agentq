package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"time"

	"github.com/shiblon/agentq/pkg/approval"
	"github.com/shiblon/agentq/pkg/auth"
	"github.com/shiblon/agentq/pkg/config"
	"github.com/shiblon/agentq/pkg/llm"
	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/agentq/pkg/store"
	"github.com/shiblon/entroq"
)

// buildSystemPrompt constructs the routing prompt from the configured agent list.
// If a rubric is provided, approval examples are added to the prompt.
func buildSystemPrompt(agents []config.Agent, rubric string) string {
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
- If a specialist reports needing permission to act, consult the approval rubric below.
`)
	if rubric != "" {
		fmt.Fprintf(&b, "\nApproval rubric:\n%s\n", strings.TrimSpace(rubric))
		b.WriteString(`
When a specialist reports needing permission:
- If the rubric allows it: use "re_dispatch" with the agent name and approved_actions.
- If the rubric requires human sign-off: use "escalate" with the agent name.
- If the rubric forbids it: use "done" with a note that the action was rejected.
`)
	}

	b.WriteString(`
Example outputs (adapt to the actual situation):
{"next": "done", "reason": "coder has produced the requested function"}
`)
	if rubric != "" {
		b.WriteString(`{"next": "re_dispatch", "agent": "coder", "approved_actions": ["write_files"], "reason": "writing to workspace is allowed per rubric"}
{"next": "escalate", "agent": "coder", "reason": "package install requires human review per rubric"}
`)
	}
	b.WriteString(`
Required fields: "next" (string), "reason" (string).
Optional: "agent" (string, for re_dispatch/escalate), "approved_actions" (array of strings, for re_dispatch).
Your response must be a JSON object with these fields only. Nothing else.
`)
	return b.String()
}

// routeDecision is the JSON shape the LLM must produce.
type routeDecision struct {
	Next            string   `json:"next"`
	Reason          string   `json:"reason"`
	Agent           string   `json:"agent,omitempty"`            // for re_dispatch and escalate
	ApprovedActions []string `json:"approved_actions,omitempty"` // for re_dispatch
}

// Supervisor is the trampoline orchestrator. It accepts a task, consults the
// LLM to decide which specialist to invoke next, and enqueues that work.
type Supervisor struct {
	client    *entroq.EntroQ
	store     *store.Store
	config    *models.AgentConfig
	llm       llm.Client
	agents    []config.Agent
	rubric    string
	exchanger          *auth.TokenExchanger         // nil means no token exchange
	issuer             *approval.Issuer             // nil means approval tokens disabled
	provenanceVerifier *approval.ProvenanceVerifier // nil means provenance not enforced
	receiptIssuer      *approval.ReceiptIssuer      // nil means dispatch receipts disabled
	receiptVerifier    *approval.ReceiptVerifier    // nil means receipt verification disabled
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

// WithRubric sets the approval policy text included in the supervisor's system
// prompt. It describes which agent actions can be auto-approved, which require
// human review, and which are rejected. Only meaningful when an LLM is configured.
func WithRubric(rubric string) Option {
	return func(s *Supervisor) { s.rubric = rubric }
}

// WithTokenExchanger enables delegated token issuance. When set, the supervisor
// exchanges the session's human token for a short-lived delegated agent token
// before dispatching each specialist task. The agent token is placed in the
// task payload under "agent_token" so the exec worker can pass it to the
// subprocess as AGENTQ_TOKEN.
func WithTokenExchanger(e *auth.TokenExchanger) Option {
	return func(s *Supervisor) { s.exchanger = e }
}

// WithProvenanceVerifier enforces session provenance. When set, every task
// processed by the supervisor must carry a valid provenance token minted by
// the API server. Tasks without a valid token are dropped.
func WithProvenanceVerifier(v *approval.ProvenanceVerifier) Option {
	return func(s *Supervisor) { s.provenanceVerifier = v }
}

// WithReceiptIssuer enables dispatch receipts. When set, the supervisor mints
// a receipt for every outgoing agent task. Exec workers carry it back in their
// return task; WithReceiptVerifier then enforces it.
func WithReceiptIssuer(ri *approval.ReceiptIssuer) Option {
	return func(s *Supervisor) { s.receiptIssuer = ri }
}

// WithReceiptVerifier enforces dispatch receipts on return tasks. When set,
// any return task (identified by a non-empty "from_agent" payload field) must
// carry a valid receipt minted by the supervisor. Tasks without a valid receipt
// are dropped.
func WithReceiptVerifier(rv *approval.ReceiptVerifier) Option {
	return func(s *Supervisor) { s.receiptVerifier = rv }
}

// WithIssuer enables Macaroon-based approval tokens. When set, the supervisor
// mints a signed token for every elevated dispatch and human-approved re-dispatch.
// Exec workers configured with a matching Verifier will enforce the token before
// applying the approval suffix to their command.
func WithIssuer(iss *approval.Issuer) Option {
	return func(s *Supervisor) { s.issuer = iss }
}

// ProcessTask handles an incoming supervisor task. It peeks at the message
// type to distinguish normal dispatches from human-review replies.
func (s *Supervisor) ProcessTask(ctx context.Context, task *entroq.Task) ([]entroq.ModifyArg, error) {
	var header struct {
		Type string `json:"type"`
	}
	// Ignore unmarshal error -- missing type field is fine, defaults to "".
	json.Unmarshal(task.Value, &header) //nolint:errcheck
	if header.Type == "review_reply" {
		return s.handleReviewReply(ctx, task)
	}
	return s.handleDispatch(ctx, task)
}

// exchangeToken attempts to exchange the human token stored in session metadata
// for a delegated agent token. Returns empty string if exchange is not
// configured or the session carries no token -- callers should treat that as
// "run without a token" rather than an error.
//
// On a successful exchange the human token is cleared from the session so it
// does not persist in the document store beyond the first use.
func (s *Supervisor) exchangeToken(ctx context.Context, session *models.Session) string {
	if s.exchanger == nil {
		return ""
	}
	humanToken := session.Meta.HumanToken
	if humanToken == "" {
		return ""
	}
	tok, err := s.exchanger.Exchange(ctx, humanToken)
	if err != nil {
		log.Printf("supervisor: token exchange for session %s: %v (continuing without token)", session.ID, err)
		return ""
	}
	// Clear the human token now that we have an agent token. Non-fatal if the
	// update fails -- the token will be ignored on the next exchange attempt
	// since the exchanger will have already consumed it.
	if err := s.store.UpdateSession(ctx, session.ID, func(s *models.Session) error {
		s.Meta.HumanToken = ""
		return nil
	}); err != nil {
		log.Printf("supervisor: clear human token for session %s: %v", session.ID, err)
	}
	return tok.AccessToken
}

// handleDispatch processes a normal agent task: loads the session, decides
// the next step, and returns the appropriate queue modifications.
func (s *Supervisor) handleDispatch(ctx context.Context, task *entroq.Task) ([]entroq.ModifyArg, error) {
	var appTask models.Task
	if err := json.Unmarshal(task.Value, &appTask); err != nil {
		return nil, fmt.Errorf("handleDispatch: unmarshal task: %w", err)
	}

	sessionID := appTask.SessionURI[len("doc:sessions/"):]

	// Verify session provenance before doing any work. Drop tasks that cannot
	// prove they originated from a legitimate API submission.
	provenanceToken, _ := appTask.Payload["provenance_token"].(string)
	if s.provenanceVerifier != nil {
		if err := s.provenanceVerifier.Verify(provenanceToken, sessionID); err != nil {
			log.Printf("supervisor: dropping task for session %s: provenance verification failed: %v", sessionID, err)
			return []entroq.ModifyArg{task.Delete()}, nil
		}
	}

	// Verify dispatch receipt on return tasks. A return task carries "from_agent"
	// in its payload; a new session submission does not. Drop returns without a
	// valid receipt -- a rogue worker cannot forge a receipt for a dispatch it
	// never received.
	fromAgent, _ := appTask.Payload["from_agent"].(string)
	if fromAgent != "" && s.receiptVerifier != nil {
		serialized, _ := appTask.Payload["dispatch_receipt"].(string)
		if _, err := s.receiptVerifier.Verify(serialized, sessionID, fromAgent); err != nil {
			log.Printf("supervisor: dropping return from %q for session %s: receipt verification failed: %v", fromAgent, sessionID, err)
			return []entroq.ModifyArg{task.Delete()}, nil
		}
	}

	// Drop tasks for cancelled sessions immediately without processing.
	{
		session, err := s.store.GetSession(ctx, sessionID)
		if err == nil && session.Status == "cancelled" {
			log.Printf("supervisor: session %s is cancelled, dropping task", sessionID)
			return []entroq.ModifyArg{task.Delete()}, nil
		}
	}

	// Compact inherited artifacts into a single summary (once per session).
	if err := s.maybeCompactInherited(ctx, sessionID); err != nil {
		log.Printf("supervisor: compact inherited for session %s: %v (continuing)", sessionID, err)
	}

	var decision routeDecision
	if err := s.store.UpdateSession(ctx, sessionID, func(session *models.Session) error {
		var err error
		decision, err = s.decide(ctx, session)
		if err != nil {
			return err
		}
		session.Artifacts = append(session.Artifacts, *s.dispatchArtifact(session.ID, decision))
		switch decision.Next {
		case "done":
			session.Status = "completed"
		case "escalate":
			session.Status = "awaiting_review"
		default:
			session.Status = "in_progress"
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("handleDispatch: update session: %w", err)
	}

	// Exchange a delegated agent token before dispatching. Non-fatal: if
	// exchange is not configured or fails, agents run without a token.
	session, err := s.store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("handleDispatch: get session for token exchange: %w", err)
	}
	agentToken := s.exchangeToken(ctx, session)

	mods := []entroq.ModifyArg{task.Delete()}

	switch decision.Next {
	case "done":
		// Nothing more to enqueue.

	case "re_dispatch":
		outQueue := s.config.GetOutputQueue(decision.Agent)
		if outQueue == "" {
			return nil, fmt.Errorf("re_dispatch: no queue for agent %q", decision.Agent)
		}
		approvalToken, err := s.mintApprovalToken(sessionID, decision.ApprovedActions)
		if err != nil {
			return nil, err
		}
		receipt, err := s.mintReceipt(sessionID, decision.Agent)
		if err != nil {
			return nil, err
		}
		payload := map[string]any{
			"agent":            decision.Agent,
			"approved_actions": decision.ApprovedActions,
		}
		if approvalToken != "" {
			payload["approval_token"] = approvalToken
		}
		if receipt != "" {
			payload["dispatch_receipt"] = receipt
		}
		if agentToken != "" {
			payload["agent_token"] = agentToken
		}
		if provenanceToken != "" {
			payload["provenance_token"] = provenanceToken
		}
		child := models.NewTask(outQueue, appTask.SessionURI, payload)
		childBytes, err := json.Marshal(child)
		if err != nil {
			return nil, fmt.Errorf("marshal re_dispatch task: %w", err)
		}
		mods = append(mods, entroq.InsertingInto(outQueue, entroq.WithRawValue(childBytes)))

	case "escalate":
		// Build a review request. The session was already loaded above.
		var summary string
		latest := latestFreshByAgent(session)
		if a, ok := latest[decision.Agent]; ok {
			summary = truncate(a.Content, 500)
		}
		req := &models.HumanReviewRequest{
			SessionURI:      appTask.SessionURI,
			RequestingAgent: decision.Agent,
			Reason:          decision.Reason,
			ReplyQueue:      "supervisor",
			ContextSummary:  summary,
			ProvenanceToken: provenanceToken,
		}
		reqBytes, err := json.Marshal(req)
		if err != nil {
			return nil, fmt.Errorf("marshal review request: %w", err)
		}
		mods = append(mods, entroq.InsertingInto("human_review", entroq.WithRawValue(reqBytes)))

	default:
		// Normal agent dispatch.
		outQueue := s.config.GetOutputQueue(decision.Next)
		if outQueue == "" {
			return nil, fmt.Errorf("no output queue for agent %q", decision.Next)
		}
		receipt, err := s.mintReceipt(sessionID, decision.Next)
		if err != nil {
			return nil, err
		}
		payload := map[string]any{
			"agent": decision.Next,
		}
		if receipt != "" {
			payload["dispatch_receipt"] = receipt
		}
		if agentToken != "" {
			payload["agent_token"] = agentToken
		}
		if provenanceToken != "" {
			payload["provenance_token"] = provenanceToken
		}
		child := models.NewTask(outQueue, appTask.SessionURI, payload)
		childBytes, err := json.Marshal(child)
		if err != nil {
			return nil, fmt.Errorf("marshal child task for %s: %w", decision.Next, err)
		}
		mods = append(mods, entroq.InsertingInto(outQueue, entroq.WithRawValue(childBytes)))
	}

	return mods, nil
}

// handleReviewReply processes a HumanReviewReply that arrives in the
// supervisor queue. It re-dispatches (if approved) or closes the session.
func (s *Supervisor) handleReviewReply(ctx context.Context, task *entroq.Task) ([]entroq.ModifyArg, error) {
	var reply models.HumanReviewReply
	if err := json.Unmarshal(task.Value, &reply); err != nil {
		return nil, fmt.Errorf("handleReviewReply: unmarshal: %w", err)
	}

	sessionID := reply.SessionURI[len("doc:sessions/"):]

	// Find the agent that was escalated by reading the last escalate artifact.
	agentName, err := s.lastEscalatedAgent(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("handleReviewReply: %w", err)
	}

	mods := []entroq.ModifyArg{task.Delete()}

	switch reply.Outcome {
	case "approved":
		outQueue := s.config.GetOutputQueue(agentName)
		if outQueue == "" {
			return nil, fmt.Errorf("handleReviewReply: no queue for agent %q", agentName)
		}
		// Record the approval in the session.
		if err := s.store.UpdateSession(ctx, sessionID, func(session *models.Session) error {
			artifact := models.NewArtifact(session.ID, "supervisor", "approval",
				fmt.Sprintf("human approved %s; input: %s", agentName, reply.HumanInput))
			session.Artifacts = append(session.Artifacts, *artifact)
			session.Status = "in_progress"
			return nil
		}); err != nil {
			return nil, fmt.Errorf("handleReviewReply: record approval: %w", err)
		}
		replySession, _ := s.store.GetSession(ctx, sessionID)
		var replyAgentToken string
		if replySession != nil {
			replyAgentToken = s.exchangeToken(ctx, replySession)
		}
		approvalToken, err := s.mintApprovalToken(sessionID, []string{"all"})
		if err != nil {
			return nil, err
		}
		receipt, err := s.mintReceipt(sessionID, agentName)
		if err != nil {
			return nil, err
		}
		replyPayload := map[string]any{
			"agent":            agentName,
			"approved_actions": []string{"all"},
			"human_input":      reply.HumanInput,
		}
		if approvalToken != "" {
			replyPayload["approval_token"] = approvalToken
		}
		if receipt != "" {
			replyPayload["dispatch_receipt"] = receipt
		}
		if replyAgentToken != "" {
			replyPayload["agent_token"] = replyAgentToken
		}
		if reply.ProvenanceToken != "" {
			replyPayload["provenance_token"] = reply.ProvenanceToken
		}
		child := models.NewTask(outQueue, reply.SessionURI, replyPayload)
		childBytes, err := json.Marshal(child)
		if err != nil {
			return nil, fmt.Errorf("marshal approved task: %w", err)
		}
		mods = append(mods, entroq.InsertingInto(outQueue, entroq.WithRawValue(childBytes)))

	default: // "rejected" or anything unexpected
		if err := s.store.UpdateSession(ctx, sessionID, func(session *models.Session) error {
			artifact := models.NewArtifact(session.ID, "supervisor", "rejection",
				fmt.Sprintf("human rejected %s: %s", agentName, reply.HumanInput))
			session.Artifacts = append(session.Artifacts, *artifact)
			session.Status = "completed"
			return nil
		}); err != nil {
			return nil, fmt.Errorf("handleReviewReply: record rejection: %w", err)
		}
	}

	return mods, nil
}

// mintReceipt mints a dispatch receipt for the given session and agent.
// Returns empty string (no error) when no receipt issuer is configured.
func (s *Supervisor) mintReceipt(sessionID, agentName string) (string, error) {
	if s.receiptIssuer == nil {
		return "", nil
	}
	r, err := s.receiptIssuer.Mint(sessionID, agentName)
	if err != nil {
		return "", fmt.Errorf("mint dispatch receipt: %w", err)
	}
	return r.Serialize()
}

// mintApprovalToken mints a Macaroon approval token for the given session and
// actions. Returns empty string (no error) when no issuer is configured.
func (s *Supervisor) mintApprovalToken(sessionID string, actions []string) (string, error) {
	if s.issuer == nil {
		return "", nil
	}
	tok, err := s.issuer.Mint(sessionID, actions, 10*time.Minute)
	if err != nil {
		return "", fmt.Errorf("mint approval token: %w", err)
	}
	return tok.Serialize()
}

// dispatchArtifact creates a supervisor dispatch artifact with JSON content
// so it can be parsed by later supervisor turns.
func (s *Supervisor) dispatchArtifact(sessionID string, d routeDecision) *models.Artifact {
	content, _ := json.Marshal(d)
	return models.NewArtifact(sessionID, "supervisor", "dispatch", string(content))
}

// lastEscalatedAgent scans session artifacts (newest first) for the most
// recent supervisor/dispatch artifact whose decision was "escalate", and
// returns the agent named in that decision.
func (s *Supervisor) lastEscalatedAgent(ctx context.Context, sessionID string) (string, error) {
	session, err := s.store.GetSession(ctx, sessionID)
	if err != nil {
		return "", fmt.Errorf("lastEscalatedAgent: %w", err)
	}
	for i := len(session.Artifacts) - 1; i >= 0; i-- {
		a := session.Artifacts[i]
		if a.AgentName != "supervisor" || a.Type != "dispatch" {
			continue
		}
		var d routeDecision
		if err := json.Unmarshal([]byte(a.Content), &d); err != nil {
			continue
		}
		if d.Next == "escalate" && d.Agent != "" {
			return d.Agent, nil
		}
	}
	return "", fmt.Errorf("no escalate dispatch found in session %s", sessionID)
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

	raw, err := s.llm.Complete(ctx, buildSystemPrompt(s.agents, s.rubric), userPrompt)
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
		// If a specialist has already produced real output, safest fallback is done.
		if hasFreshCompletedWork(session) {
			return routeDecision{Next: "done", Reason: "llm parse failed; specialist work exists"}, nil
		}
		return routeDecision{}, fmt.Errorf("parse llm response %q: %w", raw, err)
	}

	valid := map[string]bool{"done": true, "re_dispatch": true, "escalate": true}
	for _, a := range s.agents {
		valid[a.Name] = true
	}
	if !valid[d.Next] {
		return routeDecision{}, fmt.Errorf("llm returned unknown agent %q (raw: %s)", d.Next, raw)
	}

	// Rule guard: small models sometimes skip the "no prior work" rule.
	// Only fresh completed work (not permission requests, not inherited) counts.
	if !hasFreshCompletedWork(session) && (d.Next == "done" || s.looksLikeReviewer(d.Next)) {
		kw := s.decideWithKeywords(session)
		return routeDecision{Next: kw.Next, Reason: "llm rule violation corrected: " + kw.Reason}, nil
	}

	return d, nil
}

// maybeCompactInherited summarizes inherited artifacts into a single
// supervisor/compact_summary artifact, but only when all of these hold:
// - session metadata contains "compact_inherited": "true"
// - an LLM is configured
// - no compact_summary artifact already exists (idempotent)
func (s *Supervisor) maybeCompactInherited(ctx context.Context, sessionID string) error {
	if s.llm == nil {
		return nil
	}
	session, err := s.store.GetSession(ctx, sessionID)
	if err != nil {
		return err
	}
	if !session.Meta.CompactInherited {
		return nil
	}
	// Already compacted?
	for _, a := range session.Artifacts {
		if a.AgentName == "supervisor" && a.Type == "compact_summary" {
			return nil
		}
	}

	var inherited []models.Artifact
	for _, a := range session.Artifacts {
		if a.AgentName != "supervisor" && a.OriginSessionID != "" {
			inherited = append(inherited, a)
		}
	}
	if len(inherited) == 0 {
		return nil
	}

	var raw strings.Builder
	for _, a := range inherited {
		fmt.Fprintf(&raw, "[%s/%s from session %s]\n%s\n\n",
			a.AgentName, a.Type, a.OriginSessionID, a.Content)
	}
	summary, err := s.llm.Complete(ctx,
		"You are a summarizer. Given a set of agent work artifacts from prior sessions, produce a concise summary (3-6 sentences) of what was accomplished and any key outputs or decisions. Omit metadata, focus on substance.",
		"Artifacts to summarize:\n\n"+raw.String(),
	)
	if err != nil {
		return fmt.Errorf("llm compact: %w", err)
	}

	return s.store.UpdateSession(ctx, sessionID, func(session *models.Session) error {
		session.Artifacts = append(session.Artifacts,
			*models.NewArtifact(session.ID, "supervisor", "compact_summary", strings.TrimSpace(summary)))
		return nil
	})
}

// buildUserPrompt composes the context the LLM needs to make a routing decision.
func buildUserPrompt(session *models.Session) string {
	var b strings.Builder
	fmt.Fprintf(&b, "User request: %s\n\n", session.Prompt)

	// Use compact_summary if present (written by maybeCompactInherited).
	var compactSummary string
	for _, a := range session.Artifacts {
		if a.AgentName == "supervisor" && a.Type == "compact_summary" {
			compactSummary = a.Content
			break
		}
	}

	var fresh []models.Artifact
	for _, a := range session.Artifacts {
		if a.AgentName == "supervisor" {
			continue
		}
		if a.OriginSessionID == "" {
			fresh = append(fresh, a)
		}
	}

	hasInherited := compactSummary != ""
	if !hasInherited {
		for _, a := range session.Artifacts {
			if a.AgentName != "supervisor" && a.OriginSessionID != "" {
				hasInherited = true
				break
			}
		}
	}

	if !hasInherited && len(fresh) == 0 {
		b.WriteString("Work done so far: none.\n")
	} else {
		if compactSummary != "" {
			b.WriteString("Prior session context (summary):\n")
			b.WriteString(compactSummary)
			b.WriteString("\n\n")
		} else if hasInherited {
			b.WriteString("Prior session context (inherited):\n")
			for _, a := range session.Artifacts {
				if a.AgentName == "supervisor" || a.OriginSessionID == "" {
					continue
				}
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
// Matches the prompt against agent descriptions; auto-approves permission
// requests; falls back to the first non-reviewer agent if nothing matches.
func (s *Supervisor) decideWithKeywords(session *models.Session) routeDecision {
	latest := latestFreshByAgent(session)

	// Check if any agent's most recent artifact is a permission request.
	// If so, auto-approve (keyword mode is for development/testing).
	for agentName, a := range latest {
		if looksLikePermissionRequest(a.Content) {
			return routeDecision{
				Next:            "re_dispatch",
				Agent:           agentName,
				Reason:          "auto-approving permission request (keyword mode)",
				ApprovedActions: []string{"all"},
			}
		}
	}

	// If a specialist has already done fresh completed work, we're done.
	if len(latest) > 0 {
		return routeDecision{Next: "done", Reason: "specialist has completed the work"}
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

// latestFreshByAgent returns the most recent non-inherited, non-supervisor
// artifact for each agent name. Later entries in the slice overwrite earlier ones.
func latestFreshByAgent(session *models.Session) map[string]models.Artifact {
	result := make(map[string]models.Artifact)
	for _, a := range session.Artifacts {
		if a.AgentName == "supervisor" || a.OriginSessionID != "" {
			continue
		}
		result[a.AgentName] = a
	}
	return result
}

// hasFreshCompletedWork returns true if any agent has a most-recent fresh
// artifact that does NOT look like a permission request.
func hasFreshCompletedWork(session *models.Session) bool {
	for _, a := range latestFreshByAgent(session) {
		if !looksLikePermissionRequest(a.Content) {
			return true
		}
	}
	return false
}

// looksLikePermissionRequest returns true if the artifact content suggests
// the agent is blocked waiting for tool-use or action approval.
func looksLikePermissionRequest(content string) bool {
	lower := strings.ToLower(content)
	return strings.Contains(lower, "needs permission") ||
		strings.Contains(lower, "permission denied") ||
		strings.Contains(lower, "needs approval") ||
		strings.Contains(lower, "tool-use approval") ||
		strings.Contains(lower, "waiting for permission") ||
		strings.Contains(lower, "approve the file") ||
		strings.Contains(lower, "approve the write") ||
		strings.Contains(content, `"needs_review": true`) ||
		strings.Contains(content, `"needs_approval": true`)
}

// truncate returns s truncated to at most n runes, appending "..." if cut.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}
