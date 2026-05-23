// Package workflow contains higher-level session operations that are shared
// between the CLI commands and the HTTP API.
package workflow

import (
	"context"
	"fmt"
	"log"

	"github.com/shiblon/agentq/pkg/approval"
	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/agentq/pkg/store"
	"github.com/shiblon/entroq"
)

// SubmitResult is returned by SubmitSession.
type SubmitResult struct {
	SessionID  string
	SessionURI string
}

// SubmitRequest holds the parameters for a new session submission.
type SubmitRequest struct {
	UserID           string
	Prompt           string
	Messages         []models.Message // if set, used as transcript instead of wrapping Prompt
	ContinueFrom     string           // parent session ID; if set, artifacts are inherited
	Repo             string           // e.g. "github.com/shiblon/agentq"; stored as workspace_repo
	HumanToken       string           // raw bearer token; stored for delegated agent token exchange
	Compact          bool             // hint to supervisor to summarize inherited context
	ProvenanceIssuer *approval.ProvenanceIssuer // if set, mints a provenance token for this session
	// SupervisorQueue is the EntroQ queue to enqueue the supervisor task on.
	// Defaults to "agentq/supervisor/inbox" if empty.
	SupervisorQueue string
}

// SubmitSession creates a new session and enqueues it for the supervisor.
func SubmitSession(ctx context.Context, st *store.Store, eq *entroq.EntroQ, req SubmitRequest) (*SubmitResult, error) {
	session := models.NewSession(req.UserID, req.Prompt)
	session.Meta.WorkspaceRepo = req.Repo
	session.Meta.HumanToken = req.HumanToken

	if req.ContinueFrom != "" {
		parent, err := st.GetSession(ctx, req.ContinueFrom)
		if err != nil {
			return nil, fmt.Errorf("load parent session %s: %w", req.ContinueFrom, err)
		}
		session.ParentSessionID = req.ContinueFrom
		session.Meta.CompactInherited = req.Compact
		// Mint a continuation provenance token, verifying the parent's lineage.
		if req.ProvenanceIssuer != nil {
			tok, err := req.ProvenanceIssuer.MintContinuation(session.ID, req.ContinueFrom, parent.Meta.ProvenanceToken)
			if err != nil {
				return nil, fmt.Errorf("mint continuation provenance token: %w", err)
			}
			serialized, err := tok.Serialize()
			if err != nil {
				return nil, fmt.Errorf("serialize provenance token: %w", err)
			}
			session.Meta.ProvenanceToken = serialized
		}
		for _, a := range parent.Artifacts {
			// Skip supervisor dispatch artifacts -- routing decisions, not useful work.
			if a.AgentName == "supervisor" {
				continue
			}
			session.Artifacts = append(session.Artifacts, models.InheritedArtifact(session.ID, a))
		}
		log.Printf("workflow: continuing from %s, inherited %d artifact(s)", req.ContinueFrom, len(session.Artifacts))
	}

	// Mint a fresh provenance token for non-continuation sessions.
	if req.ProvenanceIssuer != nil && session.Meta.ProvenanceToken == "" {
		tok, err := req.ProvenanceIssuer.MintSession(session.ID)
		if err != nil {
			return nil, fmt.Errorf("mint provenance token: %w", err)
		}
		serialized, err := tok.Serialize()
		if err != nil {
			return nil, fmt.Errorf("serialize provenance token: %w", err)
		}
		session.Meta.ProvenanceToken = serialized
	}

	if err := st.PutSession(ctx, session); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	supervisorQueue := req.SupervisorQueue
	if supervisorQueue == "" {
		supervisorQueue = "agentq/supervisor/inbox"
	}

	sessionURI := store.SessionURI(session.ID)
	messages := req.Messages
	if messages == nil {
		messages = []models.Message{models.TextMessage("user", req.Prompt)}
	}
	payload := map[string]any{
		"messages": messages,
	}
	if session.Meta.ProvenanceToken != "" {
		payload["provenance_token"] = session.Meta.ProvenanceToken
	}
	task := models.NewTask(supervisorQueue, sessionURI, payload)

	if _, err := eq.Modify(ctx, entroq.InsertingInto(supervisorQueue, entroq.WithValue(task))); err != nil {
		return nil, fmt.Errorf("enqueue supervisor task: %w", err)
	}

	log.Printf("workflow: submitted session %s -> %s", session.ID, sessionURI)
	return &SubmitResult{SessionID: session.ID, SessionURI: sessionURI}, nil
}

// SubmitFollowUp enqueues a follow-up user message for an existing session.
// The supervisor loads its saved transcript and appends the new message.
func SubmitFollowUp(ctx context.Context, eq *entroq.EntroQ, sessionID, text, supervisorQueue string) error {
	if supervisorQueue == "" {
		supervisorQueue = "agentq/supervisor/inbox"
	}
	sessionURI := store.SessionURI(sessionID)
	task := models.NewTask(supervisorQueue, sessionURI, map[string]any{
		"follow_up": text,
	})
	_, err := eq.Modify(ctx, entroq.InsertingInto(supervisorQueue, entroq.WithValue(task)))
	return err
}
