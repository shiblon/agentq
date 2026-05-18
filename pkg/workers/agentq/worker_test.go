package agentq

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		Tools:      []string{"read_file", "write_file"},
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
		"messages":      payload.Messages,
		"workdir":       payload.Workdir,
		"blocked_tools": payload.BlockedTools,
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

// -- ExpandTools --------------------------------------------------------------

func TestExpandTools_Wildcard(t *testing.T) {
	got := ExpandTools([]string{"*"})
	if len(got) == 0 {
		t.Error("wildcard should expand to non-empty tool list")
	}
	for _, name := range got {
		if name == "*" {
			t.Error("expanded list should not contain wildcard")
		}
	}
}

func TestExpandTools_ExplicitList(t *testing.T) {
	want := []string{"read_file", "list_directory"}
	got := ExpandTools(want)
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestExpandTools_Empty(t *testing.T) {
	got := ExpandTools(nil)
	if len(got) != 0 {
		t.Errorf("empty tools should stay empty, got %v", got)
	}
}

// -- applyAllowed -------------------------------------------------------------

func TestApplyAllowed_Narrows(t *testing.T) {
	tools := []string{"read_file", "write_file", "list_directory", "create_directory"}
	got := applyAllowed(tools, []string{"read_file", "list_directory"})
	if len(got) != 2 {
		t.Fatalf("got %v, want [read_file list_directory]", got)
	}
	for _, name := range got {
		if name != "read_file" && name != "list_directory" {
			t.Errorf("unexpected tool %q in allowed result", name)
		}
	}
}

func TestApplyAllowed_Empty_ReturnsAll(t *testing.T) {
	tools := []string{"read_file", "write_file"}
	got := applyAllowed(tools, nil)
	if len(got) != len(tools) {
		t.Errorf("empty allowed should return full list, got %v", got)
	}
}

func TestApplyAllowed_AllowedNotInCeiling_Ignored(t *testing.T) {
	// Allowed contains a tool not in the configured ceiling -- it should
	// not appear in the result (intersection, not union).
	tools := []string{"read_file", "list_directory"}
	got := applyAllowed(tools, []string{"read_file", "nonexistent_tool"})
	if len(got) != 1 || got[0] != "read_file" {
		t.Errorf("got %v, want [read_file]", got)
	}
}

// -- applyBlocks ----------------------------------------------------------------

func TestApplyBlocks_RemovesBlocked(t *testing.T) {
	tools := []string{"read_file", "write_file", "list_directory"}
	got := applyBlocks(tools, []string{"write_file"})
	if len(got) != 2 {
		t.Fatalf("got %v, want [read_file list_directory]", got)
	}
	for _, name := range got {
		if name == "write_file" {
			t.Errorf("write_file should be blocked, got %v", got)
		}
	}
}

func TestApplyBlocks_NoBlocks(t *testing.T) {
	tools := []string{"read_file", "write_file"}
	got := applyBlocks(tools, nil)
	if len(got) != len(tools) {
		t.Errorf("no blocks should return full list")
	}
}

func TestApplyAllowedThenBlocks_Pipeline(t *testing.T) {
	// Full pipeline: ceiling → allowed narrows → blocked removes.
	ceiling := []string{"read_file", "write_file", "list_directory", "create_directory"}

	// Supervisor allows read+write, then blocks write for this read-only task.
	allowed := []string{"read_file", "write_file"}
	blocked := []string{"write_file"}

	got := applyBlocks(applyAllowed(ceiling, allowed), blocked)
	if len(got) != 1 || got[0] != "read_file" {
		t.Errorf("got %v, want [read_file]", got)
	}
}

// -- remarshal ----------------------------------------------------------------

func TestRemarshal_RoundTrip(t *testing.T) {
	type inner struct {
		X int    `json:"x"`
		Y string `json:"y"`
	}
	src := map[string]any{"x": float64(42), "y": "hello"}
	var dst inner
	if err := remarshal(src, &dst); err != nil {
		t.Fatalf("remarshal: %v", err)
	}
	if dst.X != 42 || dst.Y != "hello" {
		t.Errorf("got {X:%d Y:%q}, want {X:42 Y:hello}", dst.X, dst.Y)
	}
}

// -- ProcessTask unit tests ---------------------------------------------------

func TestProcessTask_MissingMessages(t *testing.T) {
	ts, _ := fakeRunnerServer(t, "irrelevant")
	w := New(testConfig(t, ts.URL))
	task, appTask := newFakeTask(t, "doc:sessions/abc", Payload{
		Workdir:  "/work",
		Messages: nil,
	})
	_, err := w.ProcessTask(context.Background(), task, appTask)
	if err == nil {
		t.Error("expected error for missing messages")
	}
}

func TestProcessTask_RunnerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "agent crashed", http.StatusInternalServerError)
	}))
	t.Cleanup(ts.Close)

	w := New(testConfig(t, ts.URL))
	task, appTask := newFakeTask(t, "doc:sessions/abc", Payload{
		Workdir:  "/work",
		Messages: []runner.Message{{Role: "user", Content: "go"}},
	})
	_, err := w.ProcessTask(context.Background(), task, appTask)
	if err == nil {
		t.Error("expected error when runner returns 500")
	}
}

func TestProcessTask_Success_ModifyArgs(t *testing.T) {
	ts, captured := fakeRunnerServer(t, "agent output here")
	priv, pubSet := testKeyPair(t)
	cfg := Config{
		Name:       "coder",
		Tools:      []string{"read_file", "write_file"},
		PrivKey:    priv,
		Issuer:     "agentq",
		MCPAddr:    "http://mcp:8081",
		RunnerURL:  ts.URL,
		ReplyQueue: "agentq/supervisor/inbox",
	}
	w := New(cfg)

	sessionURI := "doc:sessions/test-session"
	task, appTask := newFakeTask(t, sessionURI, Payload{
		Workdir: "/var/sessions/test-session",
		Messages: []runner.Message{
			{Role: "system", Content: "You are a coder."},
			{Role: "user", Content: "Write a function."},
		},
		BlockedTools: []string{"write_file"},
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
		if claims.Workdir != "/var/sessions/test-session" {
			t.Errorf("JWT workdir = %q, want /var/sessions/test-session", claims.Workdir)
		}
		// write_file was banned -- should not appear in the JWT allowlist.
		for _, tool := range claims.ToolAllowlist {
			if tool == "write_file" {
				t.Error("write_file should be banned from JWT allowlist")
			}
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

	w := New(testConfig(t, ts.URL))

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
