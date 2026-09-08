package agentq

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/shiblon/agentq/pkg/mcp"
	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/agentq/pkg/runner"
	"github.com/shiblon/entroq"
	"github.com/shiblon/entroq/pkg/backend/eqmem"
)

// testKeyPair generates an RSA private key and a matching public JWKS for tests.
func testKeyPair(t *testing.T) (priv jwk.Key, pubSet jwk.Set) {
	t.Helper()
	raw, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	priv, err = jwk.FromRaw(raw)
	if err != nil {
		t.Fatalf("jwk from raw: %v", err)
	}
	if err := priv.Set(jwk.AlgorithmKey, jwa.RS256); err != nil {
		t.Fatalf("set alg: %v", err)
	}
	if err := priv.Set(jwk.KeyIDKey, "test-key"); err != nil {
		t.Fatalf("set kid: %v", err)
	}
	pub, err := priv.PublicKey()
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	pubSet = jwk.NewSet()
	if err := pubSet.AddKey(pub); err != nil {
		t.Fatalf("add pub: %v", err)
	}
	return priv, pubSet
}

// testConfig builds a minimal Config for tests using the given runner URL.
func testConfig(t *testing.T, runnerURL string) Config {
	t.Helper()
	priv, _ := testKeyPair(t)
	return Config{
		Name:       "coder",
		Grants:     mcp.GrantSet{{Tool: "read_file"}, {Tool: "grep"}},
		PrivKey:    priv,
		Issuer:     "agentq",
		MCPAddr:    "http://mcp:8081",
		RunnerURL:  runnerURL,
		ReplyQueue: "agentq/supervisor/inbox",
	}
}

// applyArgs applies ModifyArgs to a fresh Modification for inspection.
func applyArgs(args []entroq.ModifyArg) *entroq.Modification {
	m := &entroq.Modification{}
	for _, arg := range args {
		arg(m)
	}
	return m
}

// fakeRunnerServer returns a test server that captures the RunRequest and
// responds with the given output.
func fakeRunnerServer(t *testing.T, output string) (*httptest.Server, <-chan runner.RunRequest) {
	t.Helper()
	requests := make(chan runner.RunRequest, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req runner.RunRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requests <- req
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(runner.RunResponse{Output: output})
	}))
	t.Cleanup(ts.Close)
	return ts, requests
}

// newFakeTask wraps a Payload into an entroq.Task and returns both the
// entroq task and the models.Task, mirroring what the entroq worker framework
// provides to ProcessTask after pre-unmarshaling.
func newFakeTask(t *testing.T, sessionURI string, payload Payload) (*entroq.Task, models.Task) {
	t.Helper()
	appTask := models.NewTask("agentq/coder/inbox", sessionURI, map[string]any{
		"messages": payload.Messages,
		"workdir":  payload.Workdir,
		"tools":    payload.Tools,
	})
	value, err := json.Marshal(appTask)
	if err != nil {
		t.Fatalf("marshal task: %v", err)
	}
	return &entroq.Task{
		ID:    "test-task-1",
		Queue: "agentq/coder/inbox",
		Value: value,
	}, *appTask
}

// -- narrow -------------------------------------------------------------------

func TestNarrow_EmptyRequestKeepsCeiling(t *testing.T) {
	ceiling := mcp.GrantSet{{Tool: "read_file"}, {Tool: "grep"}}
	got, err := narrow(ceiling, nil)
	if err != nil {
		t.Fatalf("narrow: %v", err)
	}
	if len(got) != len(ceiling) {
		t.Errorf("got %v, want %v", got.Tools(), ceiling.Tools())
	}
}

func TestNarrow_Attenuates(t *testing.T) {
	ceiling := mcp.GrantSet{{Tool: "read_file"}, {Tool: "write_file"}}
	got, err := narrow(ceiling, []string{"read_file"})
	if err != nil {
		t.Fatalf("narrow: %v", err)
	}
	if len(got) != 1 || got[0].Tool != "read_file" {
		t.Errorf("got %v, want [read_file]", got.Tools())
	}
}

func TestNarrow_RefusesWidening(t *testing.T) {
	// A task may not ask for a tool its agent's ceiling does not grant, and
	// the refusal is an error rather than a silent drop.
	_, err := narrow(mcp.GrantSet{{Tool: "read_file"}}, []string{"write_file"})
	if err == nil {
		t.Fatal("expected an error when a task asks to widen its ceiling")
	}
}

func TestNarrow_RejectsUngrantedTool(t *testing.T) {
	if _, err := narrow(mcp.GrantSet{{Tool: "read_file"}}, []string{"sudo"}); err == nil {
		t.Fatal("expected an error for a tool the ceiling does not grant")
	}
}

func TestProcessTask_Success_ModifyArgs(t *testing.T) {
	ts, captured := fakeRunnerServer(t, "agent output here")
	priv, pubSet := testKeyPair(t)
	cfg := Config{
		Name:       "coder",
		Grants:     mcp.GrantSet{{Tool: "read_file"}, {Tool: "grep"}},
		PrivKey:    priv,
		Issuer:     "agentq",
		MCPAddr:    "http://mcp:8081",
		RunnerURL:  ts.URL,
		ReplyQueue: "agentq/supervisor/inbox",
	}
	w := New(cfg, nil)

	sessionURI := "doc:sessions/test-session"
	task, appTask := newFakeTask(t, sessionURI, Payload{
		Workdir: "/var/sessions/test-session",
		Messages: []runner.Message{
			models.TextMessage("system", "You are a coder."),
			models.TextMessage("user", "Write a function."),
		},
		Tools: []string{"read_file"},
	})

	args, err := w.ProcessTask(context.Background(), task, appTask)
	if err != nil {
		t.Fatalf("ProcessTask: %v", err)
	}

	// Verify the runner received a JWT we can parse (with the right claims).
	select {
	case req := <-captured:
		if req.JWT == "" {
			t.Error("runner should receive a non-empty JWT")
		}
		// Parse the JWT (insecure, we just want to inspect claims).
		claims, err := mcp.ParseInsecure(req.JWT)
		if err != nil {
			t.Fatalf("ParseInsecure: %v", err)
		}
		// Scopeless config grants are rooted at the task's workdir.
		g, ok := claims.Grants.Find("read_file", "/x")
		if !ok {
			t.Fatalf("read_file not granted; grants = %v", claims.Grants.Tools())
		}
		if g.Scope.Root != "/var/sessions/test-session" {
			t.Errorf("grant root = %q, want /var/sessions/test-session", g.Scope.Root)
		}
		// The task narrowed itself to read_file, so grep must be gone even
		// though the agent's ceiling grants it.
		if slices.Contains(claims.Grants.Tools(), "grep") {
			t.Errorf("grants = %v, want grep dropped by narrowing", claims.Grants.Tools())
		}
		_ = pubSet // could also verify with Parse, but ParseInsecure is enough here
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for captured request")
	}

	// Verify ModifyArgs: one insert to reply queue, one delete.
	mod := applyArgs(args)
	if len(mod.Inserts) != 1 {
		t.Fatalf("expected 1 insert, got %d", len(mod.Inserts))
	}
	if mod.Inserts[0].Queue != "agentq/supervisor/inbox" {
		t.Errorf("insert queue = %q, want agentq/supervisor/inbox", mod.Inserts[0].Queue)
	}

	var resultTask models.Task
	if err := json.Unmarshal(mod.Inserts[0].Value, &resultTask); err != nil {
		t.Fatalf("unmarshal result task: %v", err)
	}
	if resultTask.SessionURI != sessionURI {
		t.Errorf("session_uri = %q, want %q", resultTask.SessionURI, sessionURI)
	}
	if got, _ := resultTask.Payload["output"].(string); got != "agent output here" {
		t.Errorf("output = %q, want agent output here", got)
	}
}

// -- Integration test with in-memory EntroQ -----------------------------------

func TestWorkerIntegration_QueueRoundTrip(t *testing.T) {
	ctx := context.Background()
	ts, _ := fakeRunnerServer(t, "integrated output")

	eq, err := entroq.New(ctx, eqmem.Opener())
	if err != nil {
		t.Fatalf("entroq.New: %v", err)
	}
	defer eq.Close()

	const (
		inbox      = "agentq/coder/inbox"
		replyQueue = "agentq/supervisor/inbox"
	)

	w := New(testConfig(t, ts.URL), nil)

	appTask := models.NewTask(inbox, "doc:sessions/s1", map[string]any{
		"workdir":  "/work",
		"messages": []map[string]any{{"role": "user", "content": "do the thing"}},
	})
	taskBytes, err := json.Marshal(appTask)
	if err != nil {
		t.Fatalf("marshal task: %v", err)
	}
	if _, err := eq.Modify(ctx, entroq.InsertingInto(inbox, entroq.WithRawValue(taskBytes))); err != nil {
		t.Fatalf("insert task: %v", err)
	}

	claimed, err := eq.Claim(ctx, entroq.From(inbox), entroq.ClaimFor(time.Minute))
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	var claimedAppTask models.Task
	if err := json.Unmarshal(claimed.Value, &claimedAppTask); err != nil {
		t.Fatalf("unmarshal claimed: %v", err)
	}
	modArgs, err := w.ProcessTask(ctx, claimed, claimedAppTask)
	if err != nil {
		t.Fatalf("ProcessTask: %v", err)
	}
	if _, err := eq.Modify(ctx, modArgs...); err != nil {
		t.Fatalf("apply modify: %v", err)
	}

	tasks, err := eq.Tasks(ctx, inbox)
	if err != nil {
		t.Fatalf("tasks(inbox): %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("inbox has %d tasks after processing, want 0", len(tasks))
	}

	results, err := eq.Tasks(ctx, replyQueue)
	if err != nil {
		t.Fatalf("tasks(reply): %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("reply queue has %d tasks, want 1", len(results))
	}

	var resultTask models.Task
	if err := json.Unmarshal(results[0].Value, &resultTask); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if got, _ := resultTask.Payload["output"].(string); got != "integrated output" {
		t.Errorf("output = %q, want integrated output", got)
	}
}
