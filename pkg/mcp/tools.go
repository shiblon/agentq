package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/shiblon/agentq/pkg/models"
)

// ArtifactSink receives artifacts written by the agent during a task.
type ArtifactSink func(models.Artifact)

// SessionTools returns the two fundamental tools every agent needs:
//
//   - read_session: returns the session as JSON (prompt, artifacts, metadata)
//   - write_artifact: appends an artifact via sink; agentName is fixed at construction time
//
// These tools carry no credentials and impose no side effects beyond the sink.
func SessionTools(session *models.Session, agentName string, sink ArtifactSink) []Tool {
	return []Tool{
		newReadSessionTool(session),
		newWriteArtifactTool(session, agentName, sink),
	}
}

// CollectingSink returns a sink that appends artifacts to a slice and the
// pointer to that slice. Safe to call from multiple goroutines.
func CollectingSink() (ArtifactSink, *[]models.Artifact) {
	var mu sync.Mutex
	var collected []models.Artifact
	sink := func(a models.Artifact) {
		mu.Lock()
		collected = append(collected, a)
		mu.Unlock()
	}
	return sink, &collected
}

// newReadSessionTool builds the read_session tool for the given session.
func newReadSessionTool(session *models.Session) Tool {
	def := mcp.NewTool("read_session",
		mcp.WithDescription("Read the current session: the user prompt, prior artifacts, and session metadata. Call this first to understand what you have been asked to do."),
	)
	handler := func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		b, err := json.Marshal(session)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("marshal session: %v", err)), nil
		}
		return mcp.NewToolResultText(string(b)), nil
	}
	return server.ServerTool{Tool: def, Handler: handler}
}

// newWriteArtifactTool builds the write_artifact tool for the given session and agent.
func newWriteArtifactTool(session *models.Session, agentName string, sink ArtifactSink) Tool {
	def := mcp.NewTool("write_artifact",
		mcp.WithDescription("Write a result artifact for this session. Call this when you have output to record."),
		mcp.WithString("type",
			mcp.Required(),
			mcp.Description(`Artifact type. Use "result" for primary output, "context_summary" for summaries.`),
		),
		mcp.WithString("content",
			mcp.Required(),
			mcp.Description("Artifact content as markdown text."),
		),
	)
	handler := func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		artifactType, err := req.RequireString("type")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		content, err := req.RequireString("content")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		a := models.NewArtifact(session.ID, agentName, artifactType, content)
		sink(*a)
		return mcp.NewToolResultText(fmt.Sprintf("artifact %s written", a.ID)), nil
	}
	return server.ServerTool{Tool: def, Handler: handler}
}
