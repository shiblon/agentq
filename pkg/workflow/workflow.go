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

// SubmitSession creates a new session and enqueues it for the supervisor.
// If continueFrom is non-empty, artifacts are inherited from that parent session.
// If compact is true, the supervisor will be asked to summarize inherited context.
// If repo is non-empty (e.g. "github.com/shiblon/agentq"), it is stored in
// session metadata as "workspace_repo" so exec workers know which directory to
// work in.
func SubmitSession(ctx context.Context, st *store.Store, eq *entroq.EntroQ, userID, prompt, continueFrom, repo string, compact bool) (*SubmitResult, error) {
	session := models.NewSession(userID, prompt)
	if repo != "" {
		session.Metadata["workspace_repo"] = repo
	}

	if continueFrom != "" {
		parent, err := st.GetSession(ctx, continueFrom)
		if err != nil {
			return nil, fmt.Errorf("load parent session %s: %w", continueFrom, err)
		}
		session.ParentSessionID = continueFrom
		if compact {
			session.Metadata["compact_inherited"] = true
		}
		for _, a := range parent.Artifacts {
			// Skip supervisor dispatch artifacts -- routing decisions, not useful work.
			if a.AgentName == "supervisor" {
				continue
			}
			session.Artifacts = append(session.Artifacts, models.InheritedArtifact(session.ID, a))
		}
		log.Printf("workflow: continuing from %s, inherited %d artifact(s)", continueFrom, len(session.Artifacts))
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
