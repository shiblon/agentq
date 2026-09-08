// echoagent is a deterministic test agent for end-to-end testing of the
// AgentQ → runner → MCP chain without requiring a real LLM.
//
// It reads a stream-json transcript from stdin, extracts the last user message,
// calls the MCP "echo" tool via the configured SSE endpoint, and writes a
// stream-json result event to stdout -- exactly the output the runner expects.
//
// Usage (via agentq runner serve):
//
//	agentq runner serve --command echoagent --args "" --mcp-addr http://localhost:8081
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcplib "github.com/mark3labs/mcp-go/mcp"
)

func main() {
	mcpConfigPath := flag.String("mcp-config", "", "Path to MCP config JSON file")
	// Accept the flags the runner always appends so we don't error on them.
	flag.String("input-format", "stream-json", "accepted, ignored")
	flag.String("output-format", "stream-json", "accepted, ignored")
	flag.Bool("verbose", false, "accepted, ignored")
	flag.String("tools", "", "accepted, ignored")
	flag.Bool("strict-mcp-config", false, "accepted, ignored")
	flag.String("permission-prompts", "", "accepted, ignored")
	flag.Parse()

	if err := run(*mcpConfigPath); err != nil {
		writeResult(os.Stdout, "", err)
		os.Exit(1)
	}
}

// mcpConfigFile mirrors the JSON structure written by the runner.
type mcpConfigFile struct {
	MCPServers map[string]mcpServerEntry `json:"mcpServers"`
}

type mcpServerEntry struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

func run(configPath string) error {
	entry, err := readMCPEntry(configPath)
	if err != nil {
		return err
	}

	userMsg := readLastUserMessage(os.Stdin)
	if userMsg == "" {
		userMsg = "(no user message received)"
	}

	output, err := callEcho(entry.URL, entry.Headers, userMsg)
	if err != nil {
		return err
	}

	return writeResult(os.Stdout, output, nil)
}

func readMCPEntry(configPath string) (*mcpServerEntry, error) {
	if configPath == "" {
		return nil, fmt.Errorf("--mcp-config is required")
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read mcp-config: %w", err)
	}
	var cfg mcpConfigFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse mcp-config: %w", err)
	}
	for _, entry := range cfg.MCPServers {
		if entry.URL != "" {
			e := entry // capture
			return &e, nil
		}
	}
	return nil, fmt.Errorf("no MCP server URL found in config")
}

// readLastUserMessage parses stream-json lines from r and returns the content
// of the last user message. Returns empty string if none found.
func readLastUserMessage(r *os.File) string {
	var last string
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg struct {
			Type    string `json:"type"`
			Message struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if msg.Type == "user" && len(msg.Message.Content) > 0 {
			last = msg.Message.Content[0].Text
		}
	}
	return last
}

func callEcho(mcpURL string, headers map[string]string, message string) (string, error) {
	ctx := context.Background()

	c, err := mcpclient.NewStreamableHttpClient(mcpURL,
		transport.WithHTTPHeaders(headers),
	)
	if err != nil {
		return "", fmt.Errorf("create Streamable HTTP client: %w", err)
	}
	defer c.Close()

	if err := c.Start(ctx); err != nil {
		return "", fmt.Errorf("start: %w", err)
	}
	if _, err := c.Initialize(ctx, mcplib.InitializeRequest{
		Params: mcplib.InitializeParams{
			ProtocolVersion: mcplib.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcplib.Implementation{Name: "echoagent", Version: "0.1"},
		},
	}); err != nil {
		return "", fmt.Errorf("initialize: %w", err)
	}

	result, err := c.CallTool(ctx, mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      "echo",
			Arguments: map[string]any{"message": message},
		},
	})
	if err != nil {
		return "", fmt.Errorf("call echo: %w", err)
	}
	if result.IsError {
		if len(result.Content) > 0 {
			if t, ok := result.Content[0].(mcplib.TextContent); ok {
				return "", fmt.Errorf("echo tool error: %s", t.Text)
			}
		}
		return "", fmt.Errorf("echo tool returned error")
	}
	if len(result.Content) == 0 {
		return "", nil
	}
	t, ok := result.Content[0].(mcplib.TextContent)
	if !ok {
		return "", fmt.Errorf("unexpected content type from echo")
	}
	return t.Text, nil
}

// writeResult writes a stream-json result event to w. If err is non-nil,
// writes an error result; otherwise writes a success result with output.
func writeResult(w *os.File, output string, err error) error {
	ev := map[string]any{
		"type":     "result",
		"subtype":  "success",
		"is_error": err != nil,
		"result":   output,
	}
	if err != nil {
		ev["result"] = err.Error()
	}
	return json.NewEncoder(w).Encode(ev)
}
