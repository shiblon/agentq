package mcp

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/agentq/pkg/sessionlog"
	"github.com/shiblon/entroq"
)

// AllOrchestrationTools returns the supervisor orchestration tools, backed by eq.
// namespace is the queue name prefix (e.g. "agentq"); agent queues are
// constructed as <namespace>/<agent>/inbox.
// The reply_to address for dispatched tasks is read from the JWT ReplyTo claim,
// so each supervisor instance declares its own inbox without server-side config.
// maxDepth is the maximum dispatch nesting level; 0 means unlimited.
func AllOrchestrationTools(eq *entroq.EntroQ, namespace string, maxDepth int) []server.ServerTool {
	return []server.ServerTool{
		dispatchToAgentTool(eq, namespace, maxDepth),
	}
}

func dispatchToAgentTool(eq *entroq.EntroQ, namespace string, maxDepth int) server.ServerTool {
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
		Handler: withGrantCheck("dispatch_to_agent", func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {

			c := claimsFromContext(ctx) // non-nil guaranteed by withGrantCheck

			if maxDepth > 0 && c.Depth >= maxDepth {
				return mcplib.NewToolResultError(fmt.Sprintf(
					"dispatch depth limit reached (current=%d, max=%d): agents at this level may not dispatch further",
					c.Depth, maxDepth,
				)), nil
			}

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

			sessionURI := c.SessionID
			parentSessionID := strings.TrimPrefix(sessionURI, "doc:sessions/")

			var raw [8]byte
			if _, err := rand.Read(raw[:]); err != nil {
				return mcplib.NewToolResultError(fmt.Sprintf("generate child session ID: %v", err)), nil
			}
			childSessionID := fmt.Sprintf("%016x", binary.BigEndian.Uint64(raw[:]))

			task := models.NewTask(targetQueue, sessionURI,
				map[string]any{
					"workdir":           workdir,
					"messages":          messages,
					"parent_session_id": parentSessionID,
					"child_session_id":  childSessionID,
					"depth":             c.Depth + 1,
				},
				models.WithReplyTo(c.ReplyTo),
			)

			if _, err := eq.Modify(ctx,
				entroq.InsertingInto(targetQueue, entroq.WithValue(task)),
				sessionlog.AppendArg(parentSessionID, sessionlog.Chunk{
					Type:    sessionlog.ChunkDispatchPending,
					Agent:   agentName,
					ChildID: childSessionID,
				}),
				sessionlog.PendingAddArg(parentSessionID, childSessionID, agentName),
			); err != nil {
				return mcplib.NewToolResultError(fmt.Sprintf("dispatch to %s: %v", agentName, err)), nil
			}

			return mcplib.NewToolResultText(fmt.Sprintf("dispatched to %s (child session: %s)", agentName, childSessionID)), nil
		}),
	}
}
