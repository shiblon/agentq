// Package smoke contains end-to-end tests against a live in-process eq server.
package smoke

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/agentq/pkg/store"
	"github.com/shiblon/agentq/pkg/workers/mock"
	"github.com/shiblon/agentq/pkg/workers/supervisor"
	"github.com/shiblon/entroq"
	"github.com/shiblon/entroq/pkg/backend/eqmem"
	"github.com/shiblon/entroq/pkg/testing/eqtest"
)

// newEQ creates an in-process in-memory entroq client for testing.
func newEQ(t *testing.T) *entroq.EntroQ {
	t.Helper()
	ctx := context.Background()
	eq, err := entroq.New(ctx, eqmem.Opener())
	if err != nil {
		t.Fatalf("new eq: %v", err)
	}
	t.Cleanup(func() { eq.Close() })
	return eq
}

// newGRPCEQ creates a gRPC client backed by an in-process eqmem service.
func newGRPCEQ(t *testing.T) *entroq.EntroQ {
	t.Helper()
	ctx := context.Background()
	eq, stop, err := eqtest.ClientService(ctx, eqmem.Opener())
	if err != nil {
		t.Fatalf("new grpc eq: %v", err)
	}
	t.Cleanup(stop)
	return eq
}

func TestSessionRoundTrip(t *testing.T) {
	ctx := context.Background()
	eq := newEQ(t)
	st := store.New(eq)

	session := models.NewSession("testuser", "implement a login page")
	if err := st.PutSession(ctx, session); err != nil {
		t.Fatalf("PutSession: %v", err)
	}

	got, err := st.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.ID != session.ID {
		t.Errorf("ID: got %q, want %q", got.ID, session.ID)
	}
	if got.Prompt != session.Prompt {
		t.Errorf("Prompt: got %q, want %q", got.Prompt, session.Prompt)
	}
}

func TestSessionUpdate(t *testing.T) {
	ctx := context.Background()
	eq := newEQ(t)
	st := store.New(eq)

	session := models.NewSession("testuser", "implement a login page")
	if err := st.PutSession(ctx, session); err != nil {
		t.Fatalf("PutSession (create): %v", err)
	}

	artifact := models.NewArtifact(session.ID, "supervisor", "dispatch", "routed to coder")
	session.Artifacts = append(session.Artifacts, *artifact)
	if err := st.PutSession(ctx, session); err != nil {
		t.Fatalf("PutSession (update): %v", err)
	}

	got, err := st.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession after update: %v", err)
	}
	if len(got.Artifacts) != 1 {
		t.Errorf("artifacts: got %d, want 1", len(got.Artifacts))
	}
}

func TestSessionRoundTripGRPC(t *testing.T) {
	ctx := context.Background()
	eq := newGRPCEQ(t)
	st := store.New(eq)

	session := models.NewSession("testuser", "implement a login page")
	if err := st.PutSession(ctx, session); err != nil {
		t.Fatalf("PutSession: %v", err)
	}

	got, err := st.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.ID != session.ID {
		t.Errorf("ID: got %q, want %q", got.ID, session.ID)
	}
	if got.Prompt != session.Prompt {
		t.Errorf("Prompt: got %q, want %q", got.Prompt, session.Prompt)
	}
}

func TestSessionURI(t *testing.T) {
	id := "abc123"
	uri := store.SessionURI(id)
	if uri != "doc:sessions/abc123" {
		t.Errorf("SessionURI: got %q, want %q", uri, "doc:sessions/abc123")
	}
}

func TestSupervisorEndToEnd(t *testing.T) {
	ctx := context.Background()
	eq := newGRPCEQ(t)
	st := store.New(eq)

	// Submit a session.
	session := models.NewSession("testuser", "implement a login page")
	if err := st.PutSession(ctx, session); err != nil {
		t.Fatalf("PutSession: %v", err)
	}
	sessionURI := store.SessionURI(session.ID)

	// Insert the supervisor task.
	task := models.NewTask("supervisor", sessionURI, nil)
	taskBytes, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("marshal task: %v", err)
	}
	if _, err := eq.Modify(ctx, entroq.InsertingInto("supervisor", entroq.WithRawValue(taskBytes))); err != nil {
		t.Fatalf("insert supervisor task: %v", err)
	}

	// Claim and process as supervisor.
	claimed, err := eq.Claim(ctx, entroq.From("supervisor"))
	if err != nil {
		t.Fatalf("claim supervisor task: %v", err)
	}

	sup := supervisor.New(eq, supervisor.WithConfig(func(c *models.AgentConfig) {
		*c = *models.SupervisorAgent()
	}))
	mods, err := sup.ProcessTask(ctx, claimed)
	if err != nil {
		t.Fatalf("supervisor ProcessTask: %v", err)
	}
	if _, err := eq.Modify(ctx, mods...); err != nil {
		t.Fatalf("supervisor Modify: %v", err)
	}

	// Verify session has a dispatch artifact.
	got, err := st.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession after supervisor: %v", err)
	}
	if got.Status != "in_progress" {
		t.Errorf("status: got %q, want %q", got.Status, "in_progress")
	}
	if len(got.Artifacts) != 1 {
		t.Errorf("artifacts: got %d, want 1", len(got.Artifacts))
	} else if got.Artifacts[0].Type != "dispatch" {
		t.Errorf("artifact type: got %q, want %q", got.Artifacts[0].Type, "dispatch")
	}

	// Verify coder task was enqueued (prompt contains "implement").
	coderTask, err := eq.TryClaim(ctx, entroq.From("coder_queue"))
	if err != nil {
		t.Fatalf("try claim coder_queue: %v", err)
	}
	if coderTask == nil {
		t.Fatal("expected a task in coder_queue, got none")
	}

	// Process it as the mock coder.
	coderMods, err := mock.New("coder", eq).ProcessTask(ctx, coderTask)
	if err != nil {
		t.Fatalf("coder ProcessTask: %v", err)
	}
	if _, err := eq.Modify(ctx, coderMods...); err != nil {
		t.Fatalf("coder Modify: %v", err)
	}

	// Verify coder artifact landed in session.
	final, err := st.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession after coder: %v", err)
	}
	if len(final.Artifacts) != 2 {
		t.Errorf("final artifacts: got %d, want 2", len(final.Artifacts))
	}
}
