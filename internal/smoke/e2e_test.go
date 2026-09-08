package smoke

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/shiblon/agentq/pkg/mcp"
	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/agentq/pkg/runner"
	agentqworker "github.com/shiblon/agentq/pkg/workers/agentq"
	"github.com/shiblon/entroq"
	eqworker "github.com/shiblon/entroq/pkg/worker"
)

// Register the flags that the runner always appends to agent subprocess args.
// Without these, flag.Parse() would fail before TestMain is reached when this
// binary is invoked as a subprocess.
var (
	_ = flag.String("mcp-config", "", "MCP config file path (runner-injected)")
	_ = flag.String("input-format", "", "ignored by echo agent")
	_ = flag.String("output-format", "", "ignored by echo agent")
	_ = flag.Bool("verbose", false, "ignored by echo agent")
)

// TestMain detects when the test binary is being invoked as a fake agent
// subprocess (AGENTQ_E2E_ECHO_AGENT=1) and runs the echoagent logic instead
// of the test suite. This lets the runner use the test binary itself as the
// agent command, avoiding any dependency on an external binary.
func TestMain(m *testing.M) {
	if os.Getenv("AGENTQ_E2E_ECHO_AGENT") == "1" {
		runAsEchoAgent()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runAsEchoAgent implements the minimal echoagent behaviour inline:
// read --mcp-config, read stdin for the last user message, call MCP echo,
// print stream-json result.
func runAsEchoAgent() {
	var mcpConfigPath string
	for i, arg := range os.Args {
		if arg == "--mcp-config" && i+1 < len(os.Args) {
			mcpConfigPath = os.Args[i+1]
			break
		}
	}
	if mcpConfigPath == "" {
		printEchoResult("", fmt.Errorf("--mcp-config not found in args"))
		return
	}

	entry, err := readE2EMCPEntry(mcpConfigPath)
	if err != nil {
		printEchoResult("", err)
		return
	}

	output, err := callE2EEcho(entry.URL, entry.Headers)
	if err != nil {
		printEchoResult("", err)
		return
	}
	printEchoResult(output, nil)
}

type e2eMCPEntry struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

type e2eMCPConfig struct {
	MCPServers map[string]e2eMCPEntry `json:"mcpServers"`
}

func readE2EMCPEntry(path string) (*e2eMCPEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read mcp-config: %w", err)
	}
	var cfg e2eMCPConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse mcp-config: %w", err)
	}
	for _, entry := range cfg.MCPServers {
		e := entry
		return &e, nil
	}
	return nil, fmt.Errorf("no MCP server entry found")
}

func callE2EEcho(mcpURL string, headers map[string]string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c, err := client.NewStreamableHttpClient(mcpURL,
		transport.WithHTTPHeaders(headers),
	)
	if err != nil {
		return "", fmt.Errorf("create client: %w", err)
	}
	defer c.Close()

	if err := c.Start(ctx); err != nil {
		return "", fmt.Errorf("start: %w", err)
	}
	if _, err := c.Initialize(ctx, mcplib.InitializeRequest{
		Params: mcplib.InitializeParams{
			ProtocolVersion: mcplib.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcplib.Implementation{Name: "e2e-echo", Version: "0.1"},
		},
	}); err != nil {
		return "", fmt.Errorf("initialize: %w", err)
	}
	result, err := c.CallTool(ctx, mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      "echo",
			Arguments: map[string]any{"message": "e2e ping"},
		},
	})
	if err != nil {
		return "", fmt.Errorf("call echo: %w", err)
	}
	if result.IsError {
		return "", fmt.Errorf("echo tool error")
	}
	if len(result.Content) == 0 {
		return "", nil
	}
	if t, ok := result.Content[0].(mcplib.TextContent); ok {
		return t.Text, nil
	}
	return "", fmt.Errorf("unexpected content type")
}

func printEchoResult(output string, err error) {
	ev := map[string]any{
		"type":     "result",
		"subtype":  "success",
		"is_error": err != nil,
		"result":   output,
	}
	if err != nil {
		ev["result"] = err.Error()
	}
	json.NewEncoder(os.Stdout).Encode(ev)
}

// -- the actual end-to-end test -----------------------------------------------

func TestE2E_AgentQWorkerThroughRunnerToMCP(t *testing.T) {
	// Tell subprocess invocations of this test binary to act as the echo agent.
	t.Setenv("AGENTQ_E2E_ECHO_AGENT", "1")

	ctx := context.Background()

	// 1. MCP server -- insecure mode + dev tools (echo tool).
	mcpSrv, err := mcp.New(mcp.Config{
		Addr:                     "127.0.0.1:0",
		InsecureSkipVerification: true,
		DevTools:                 true,
	})
	if err != nil {
		t.Fatalf("mcp.New: %v", err)
	}
	mcpTS := httptest.NewServer(mcpSrv.Handler())
	t.Cleanup(mcpTS.Close)

	// 2. Runner -- uses this test binary as the agent command.
	// AGENTQ_E2E_ECHO_AGENT=1 triggers the echoagent path in TestMain.
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	r := runner.New(runner.Config{
		Command: exe,
		Args:    nil, // TestMain exits early via AGENTQ_E2E_ECHO_AGENT before any tests run
		MCPAddr: mcpTS.URL,
	})
	runnerTS := httptest.NewServer(r)
	t.Cleanup(runnerTS.Close)

	// 3. AgentQ worker -- ephemeral key, in-memory EntroQ.
	kp, err := mcp.GenerateEphemeralKey()
	if err != nil {
		t.Fatalf("GenerateEphemeralKey: %v", err)
	}

	const (
		inbox      = "agentq/coder/inbox"
		replyQueue = "agentq/supervisor/inbox"
	)

	eq := newEQ(t)
	w := agentqworker.New(agentqworker.Config{
		Name:       "coder",
		Legs:       mcp.Legs(),
		PrivKey:    kp.Private,
		Issuer:     "agentq",
		MCPAddr:    mcpTS.URL,
		RunnerURL:  runnerTS.URL,
		ReplyQueue: replyQueue,
	}, eq)

	// Start the worker loop in the background.
	workerCtx, cancelWorker := context.WithCancel(ctx)
	t.Cleanup(cancelWorker)
	workerDone := make(chan error, 1)
	go func() {
		workerDone <- eqworker.New(eq,
			eqworker.WithDoModify(func(ctx context.Context, task *entroq.Task, appTask models.Task, _ []*entroq.Doc) ([]entroq.ModifyArg, error) {
				return w.ProcessTask(ctx, task, appTask)
			}),
		).Run(workerCtx, eqworker.Watching(inbox))
	}()

	// 4. Insert a task.
	appTask := models.NewTask(inbox, "doc:sessions/e2e-test", map[string]any{
		"workdir":  filepath.Join(t.TempDir()),
		"messages": []map[string]any{{"role": "user", "content": "hello e2e"}},
	})
	taskBytes, err := json.Marshal(appTask)
	if err != nil {
		t.Fatalf("marshal task: %v", err)
	}
	if _, err := eq.Modify(ctx, entroq.InsertingInto(inbox, entroq.WithRawValue(taskBytes))); err != nil {
		t.Fatalf("insert task: %v", err)
	}

	// 5. Wait for result on reply queue (up to 30 seconds for subprocess overhead).
	deadline := time.Now().Add(30 * time.Second)
	var resultTask models.Task
	for {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for result task")
		}
		tasks, err := eq.Tasks(ctx, replyQueue)
		if err != nil {
			t.Fatalf("tasks: %v", err)
		}
		if len(tasks) > 0 {
			if err := json.Unmarshal(tasks[0].Value, &resultTask); err != nil {
				t.Fatalf("unmarshal result: %v", err)
			}
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	cancelWorker()

	// 6. Verify the result.
	if got, _ := resultTask.Payload["from_agent"].(string); got != "coder" {
		t.Errorf("from_agent = %q, want coder", got)
	}
	output, _ := resultTask.Payload["output"].(string)
	if output != "e2e ping" {
		t.Errorf("output = %q, want e2e ping", output)
	}
	t.Logf("e2e result: %s", output)
}
