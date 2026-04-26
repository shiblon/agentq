// Package workflow contains higher-level session operations that are shared
// between the CLI commands and the HTTP API.
package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

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
	UserID       string
	Prompt       string
	ContinueFrom string // parent session ID; if set, artifacts are inherited
	Repo         string // e.g. "github.com/shiblon/agentq"; stored as workspace_repo
	HumanToken   string // raw bearer token; stored for delegated agent token exchange
	Compact      bool   // hint to supervisor to summarize inherited context
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
		for _, a := range parent.Artifacts {
			// Skip supervisor dispatch artifacts -- routing decisions, not useful work.
			if a.AgentName == "supervisor" {
				continue
			}
			session.Artifacts = append(session.Artifacts, models.InheritedArtifact(session.ID, a))
		}
		log.Printf("workflow: continuing from %s, inherited %d artifact(s)", req.ContinueFrom, len(session.Artifacts))
	}

	if err := st.PutSession(ctx, session); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	sessionURI := store.SessionURI(session.ID)
	task := models.NewTask("supervisor", sessionURI, nil)
	taskBytes, err := json.Marshal(task)
	if err != nil {
		return nil, fmt.Errorf("marshal supervisor task: %w", err)
	}

	if _, err := eq.Modify(ctx, entroq.InsertingInto("supervisor", entroq.WithRawValue(taskBytes))); err != nil {
		return nil, fmt.Errorf("enqueue supervisor task: %w", err)
	}

	log.Printf("workflow: submitted session %s -> %s", session.ID, sessionURI)
	return &SubmitResult{SessionID: session.ID, SessionURI: sessionURI}, nil
}
