package runner

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/shiblon/agentq/pkg/models"
)

// fakeAgent writes a shell script to dir that discards stdin and args, then
// emits a single stream-json result event containing output. Returns the
// script path. Requires a POSIX shell at /bin/sh.
func fakeAgent(t *testing.T, output string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-agent.sh")

	b, err := json.Marshal(map[string]any{
		"type":     "result",
		"subtype":  "success",
		"is_error": false,
		"result":   output,
	})
	if err != nil {
		t.Fatalf("marshal fake result: %v", err)
	}

	// Discard stdin; ignore all args (--mcp-config, --input-format, etc.).
	script := "#!/bin/sh\ncat > /dev/null\nprintf '%s\\n' " + "'" + string(b) + "'\n"
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("write fake agent: %v", err)
	}
	return path
}

func postRunRequest(t *testing.T, url string, req RunRequest) *RunResponse {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var result RunResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return &result
}

func TestRunnerIntegration_BasicRoundTrip(t *testing.T) {
	agent := fakeAgent(t, "the agent said this")

	r := New(Config{
		Command: "/bin/sh",
		Args:    []string{agent},
		MCPAddr: "http://unused:1234", // not contacted; fake agent ignores --mcp-config
	})
	ts := httptest.NewServer(r)
	defer ts.Close()

	result := postRunRequest(t, ts.URL, RunRequest{
		JWT: "fake-jwt",
		Messages: []Message{
			models.TextMessage("system", "You are a test agent."),
			models.TextMessage("user", "Say something."),
		},
	})

	if result.Output != "the agent said this" {
		t.Errorf("output = %q, want %q", result.Output, "the agent said this")
	}
}

func TestRunnerIntegration_MissingJWT_Returns400(t *testing.T) {
	agent := fakeAgent(t, "irrelevant")
	r := New(Config{Command: "/bin/sh", Args: []string{agent}, MCPAddr: "http://unused:1234"})
	ts := httptest.NewServer(r)
	defer ts.Close()

	body, _ := json.Marshal(RunRequest{Messages: []Message{models.TextMessage("user", "hi")}})
	resp, err := http.Post(ts.URL, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestRunnerIntegration_EmptyMessages_Returns400(t *testing.T) {
	agent := fakeAgent(t, "irrelevant")
	r := New(Config{Command: "/bin/sh", Args: []string{agent}, MCPAddr: "http://unused:1234"})
	ts := httptest.NewServer(r)
	defer ts.Close()

	body, _ := json.Marshal(RunRequest{JWT: "fake-jwt"})
	resp, err := http.Post(ts.URL, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestRunnerIntegration_MCPConfigWritten(t *testing.T) {
	// Verify the runner actually writes a --mcp-config file and passes its
	// path to the agent. The fake agent logs its args to a temp file so we
	// can inspect them.
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	dir := t.TempDir()
	script := "#!/bin/sh\ncat > /dev/null\necho \"$@\" > " + argsFile + "\nprintf '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"ok\"}\\n'\n"
	scriptPath := filepath.Join(dir, "spy-agent.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	r := New(Config{
		Command: "/bin/sh",
		Args:    []string{scriptPath},
		MCPAddr: "http://mcp-service:8081",
	})
	ts := httptest.NewServer(r)
	defer ts.Close()

	postRunRequest(t, ts.URL, RunRequest{
		JWT:      "my-test-jwt",
		Messages: []Message{models.TextMessage("user", "go")},
	})

	argsData, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args file: %v", err)
	}
	args := string(argsData)

	if !bytes.Contains(argsData, []byte("--mcp-config")) {
		t.Errorf("expected --mcp-config in args, got: %s", args)
	}
	if !bytes.Contains(argsData, []byte("--input-format")) {
		t.Errorf("expected --input-format in args, got: %s", args)
	}
}
