package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/entroq"
)

// AllOrchestrationTools returns the supervisor orchestration tools, backed by eq.
// namespace is the queue name prefix (e.g. "agentq"); agent queues are
// constructed as <namespace>/<agent>/inbox.
// The reply_to address for dispatched tasks is read from the JWT ReplyTo claim,
// so each supervisor instance declares its own inbox without server-side config.
func AllOrchestrationTools(eq *entroq.EntroQ, namespace string) []server.ServerTool {
	return []server.ServerTool{
		dispatchToAgentTool(eq, namespace),
	}
}

func dispatchToAgentTool(eq *entroq.EntroQ, namespace string) server.ServerTool {
	def := mcplib.NewTool("dispatch_to_agent",
		mcplib.WithDescription(
			"Dispatch a task to a named leaf agent. Fire-and-forget: returns immediately "+
				"after inserting the task into the agent's queue. The agent's result will "+
				"arrive as a new task on the supervisor queue for any supervisor to process."),
		mcplib.WithString("agent",
			mcplib.Required(),
			mcplib.Description("Agent name, e.g. 'coder' or 'researcher'"),
		),
		mcplib.WithString("workdir",
			mcplib.Required(),
			mcplib.Description("Working directory for the agent session"),
		),
		mcplib.WithObject("messages",
			mcplib.Required(),
			mcplib.Description("Conversation transcript as a JSON array of {role, content} objects"),
		),
	)
	return server.ServerTool{
		Tool: def,
		Handler: withAllowlistCheck("dispatch_to_agent", func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
			c := claimsFromContext(ctx) // non-nil guaranteed by withAllowlistCheck

			agentName, err := req.RequireString("agent")
			if err != nil {
				return mcplib.NewToolResultError(err.Error()), nil
			}
			workdir, err := req.RequireString("workdir")
			if err != nil {
				return mcplib.NewToolResultError(err.Error()), nil
			}

			// Extract messages from the request arguments.
			args, ok := req.Params.Arguments.(map[string]any)
			if !ok {
				return mcplib.NewToolResultError("invalid arguments"), nil
			}
			messagesRaw, ok := args["messages"]
			if !ok {
				return mcplib.NewToolResultError("missing messages"), nil
			}
			// Re-encode to JSON and decode as []runner.Message equivalent.
			// We store as []any to avoid importing pkg/runner from pkg/mcp.
			msgBytes, err := json.Marshal(messagesRaw)
			if err != nil {
				return mcplib.NewToolResultError(fmt.Sprintf("encode messages: %v", err)), nil
			}
			var messages []any
			if err := json.Unmarshal(msgBytes, &messages); err != nil {
				return mcplib.NewToolResultError(fmt.Sprintf("decode messages: %v", err)), nil
			}

			targetQueue := fmt.Sprintf("%s/%s/inbox", namespace, agentName)
			if c.ReplyTo == "" {
				return mcplib.NewToolResultError("JWT missing mcp_reply_to claim; supervisor must mint JWT with ReplyTo set"), nil
			}

			sessionURI := "doc:sessions/" + c.SessionID

			task := models.NewTask(targetQueue, sessionURI,
				map[string]any{
					"workdir":  workdir,
					"messages": messages,
				},
				models.WithReplyTo(c.ReplyTo),
			)

			if _, err := eq.Modify(ctx, entroq.InsertingInto(targetQueue, entroq.WithValue(task))); err != nil {
				return mcplib.NewToolResultError(fmt.Sprintf("dispatch to %s: %v", agentName, err)), nil
			}

			return mcplib.NewToolResultText(fmt.Sprintf("dispatched to %s (session %s)", agentName, c.SessionID)), nil
		}),
	}
}
