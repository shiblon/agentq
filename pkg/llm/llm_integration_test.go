package llm_test

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shiblon/agentq/pkg/llm"
)

// ollamaBase returns the Ollama base URL from OLLAMA_HOST env, defaulting to
// the docker-compose port on localhost.
func ollamaBase() string {
	if h := os.Getenv("OLLAMA_HOST"); h != "" {
		return h
	}
	return "http://localhost:11434"
}

// ollamaAvailable does a quick HEAD against /api/tags to see if Ollama is up.
func ollamaAvailable(base string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/tags", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func TestOllamaComplete_Integration(t *testing.T) {
	base := ollamaBase()
	if !ollamaAvailable(base) {
		t.Skipf("Ollama not reachable at %s -- start with: docker compose up -d ollama", base)
	}

	model := os.Getenv("OLLAMA_MODEL")
	if model == "" {
		model = "qwen2.5:0.5b"
	}

	client := llm.NewOllamaClient(llm.OllamaConfig{
		BaseURL: base,
		Model:   model,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	reply, err := client.Complete(ctx,
		"You are a helpful assistant. Keep answers short.",
		"In one sentence, what is 2+2?",
	)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if strings.TrimSpace(reply) == "" {
		t.Fatal("got empty reply")
	}
	t.Logf("model=%s reply=%q", model, reply)
}
