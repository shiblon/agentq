// Package smoke contains end-to-end tests against a live in-process eq server.
package smoke

import (
	"context"
	"testing"

	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/agentq/pkg/store"
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
