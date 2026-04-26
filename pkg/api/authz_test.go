package api

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// loadPolicyModules reads the deploy/policy/*.rego files relative to the
// module root, for use in tests that want to exercise the real policy.
func loadPolicyModules(t *testing.T) map[string]string {
	t.Helper()
	dir := filepath.Join("..", "..", "deploy", "policy")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read policy dir %q: %v", dir, err)
	}
	modules := make(map[string]string)
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".rego" {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		modules[e.Name()] = string(src)
	}
	return modules
}

func newTestAuthorizer(t *testing.T, modules map[string]string) *OPAAuthorizer {
	t.Helper()
	a, err := NewOPAAuthorizerFromModules(context.Background(), modules)
	if err != nil {
		t.Fatalf("new opa authorizer: %v", err)
	}
	return a
}

func claimsCtx(claims *Claims) context.Context {
	return context.WithValue(context.Background(), claimsKey{}, claims)
}

func TestOPAAuthorizer_HumanAllowed(t *testing.T) {
	a := newTestAuthorizer(t, loadPolicyModules(t))
	r := httptest.NewRequest("POST", "/api/v1/sessions", nil)
	ctx := claimsCtx(&Claims{Subject: "human-123", IsAgent: false})

	ok, err := a.Allow(ctx, r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("human should be allowed to POST sessions")
	}
}

func TestOPAAuthorizer_AgentAllowedSessionPost(t *testing.T) {
	a := newTestAuthorizer(t, loadPolicyModules(t))
	r := httptest.NewRequest("POST", "/api/v1/sessions", nil)
	ctx := claimsCtx(&Claims{Subject: "agent-456", IsAgent: true, DelegatedBy: "human-123"})

	ok, err := a.Allow(ctx, r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("agent should be allowed to POST sessions")
	}
}

func TestOPAAuthorizer_AgentDeniedReviewApproval(t *testing.T) {
	a := newTestAuthorizer(t, loadPolicyModules(t))
	r := httptest.NewRequest("POST", "/api/v1/reviews/abc/approve", nil)
	ctx := claimsCtx(&Claims{Subject: "agent-456", IsAgent: true, DelegatedBy: "human-123"})

	ok, err := a.Allow(ctx, r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("agent should not be allowed to approve reviews")
	}
}

func TestOPAAuthorizer_AgentDeniedAdminEndpoint(t *testing.T) {
	a := newTestAuthorizer(t, loadPolicyModules(t))
	r := httptest.NewRequest("DELETE", "/api/v1/agents/some-agent", nil)
	ctx := claimsCtx(&Claims{Subject: "agent-456", IsAgent: true, DelegatedBy: "human-123"})

	ok, err := a.Allow(ctx, r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("agent should not be allowed to DELETE agents")
	}
}

func TestOPAAuthorizer_NoPrincipalDenied(t *testing.T) {
	a := newTestAuthorizer(t, loadPolicyModules(t))
	r := httptest.NewRequest("GET", "/api/v1/sessions", nil)

	ok, err := a.Allow(context.Background(), r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("request with no principal should be denied")
	}
}

func TestOPAAuthorizer_HumanAllowedReviewApproval(t *testing.T) {
	a := newTestAuthorizer(t, loadPolicyModules(t))
	r := httptest.NewRequest("POST", "/api/v1/reviews/abc/approve", nil)
	ctx := claimsCtx(&Claims{Subject: "human-123", IsAgent: false})

	ok, err := a.Allow(ctx, r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("human should be allowed to approve reviews")
	}
}
