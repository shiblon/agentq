// Package mock provides a generic mock agent worker.
// It claims tasks, appends a stub artifact to the session, and deletes the task.
// Used for all non-supervisor agent roles during prototyping.
package mock

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/agentq/pkg/store"
	"github.com/shiblon/entroq"
)

// Worker is a mock agent that acknowledges tasks without doing real work.
type Worker struct {
	name  string
	store *store.Store
}

// New creates a mock worker for the given agent name.
func New(name string, eq *entroq.EntroQ) *Worker {
	return &Worker{
		name:  name,
		store: store.New(eq),
	}
}

// ProcessTask claims a task, appends a stub artifact to the session, and deletes the task.
// Returns the modifications to apply atomically.
func (w *Worker) ProcessTask(ctx context.Context, task *entroq.Task) ([]entroq.ModifyArg, error) {
	var appTask models.Task
	if err := json.Unmarshal(task.Value, &appTask); err != nil {
		return nil, fmt.Errorf("mock %s: unmarshal task: %w", w.name, err)
	}

	sessionURI := appTask.SessionURI
	sessionID := sessionURI[len("doc:sessions/"):]

	if err := w.store.UpdateSession(ctx, sessionID, func(session *models.Session) error {
		artifact := models.NewArtifact(
			session.ID,
			w.name,
			"result",
			fmt.Sprintf("Mock %s completed. Prompt was: %s\n", w.name, session.Prompt),
		)
		session.Artifacts = append(session.Artifacts, *artifact)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("mock %s: update session: %w", w.name, err)
	}

	// Return the work to the supervisor for the next routing decision.
	returnTask := models.NewTask("supervisor", appTask.SessionURI, map[string]any{
		"from_agent": w.name,
	})
	returnBytes, err := json.Marshal(returnTask)
	if err != nil {
		return nil, fmt.Errorf("mock %s: marshal return task: %w", w.name, err)
	}

	return []entroq.ModifyArg{
		entroq.InsertingInto("supervisor", entroq.WithRawValue(returnBytes)),
		task.Delete(),
	}, nil
}
