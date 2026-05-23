// Package sessionlog manages a session's ordered chunk log and pending dispatch
// set in the EntroQ docstore.
//
// A session's state lives in two docstore namespaces:
//
//	sessions/log     — append-only event chunks, ordered by secondary key
//	sessions/pending — in-flight dispatch entries, primary key = parent session ID, doc ID = child session ID
//
// All write operations return [entroq.ModifyArg] values so callers can compose
// them with queue operations in a single atomic [entroq.EntroQ.Modify] call.
package sessionlog

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/entroq"
)

const (
	nsLog     = "sessions/log"
	nsPending = "sessions/pending"
)

// ChunkType identifies the kind of event stored in a chunk.
type ChunkType string

const (
	ChunkUserMessage      ChunkType = "user_message"
	ChunkAssistantMessage ChunkType = "assistant_message"
	ChunkDispatchPending  ChunkType = "dispatch_pending"
	ChunkDispatchComplete ChunkType = "dispatch_complete"
)

// Chunk is a single entry in a session's ordered event log.
type Chunk struct {
	Type      ChunkType             `json:"type"`
	Content   string                `json:"content,omitempty"`  // message text, for message types
	Agent     string                `json:"agent,omitempty"`    // agent name, for dispatch types
	ChildID   string                `json:"child_id,omitempty"` // child session ID, for dispatch types
	Summary   string                `json:"summary,omitempty"`  // result summary, for dispatch_complete (legacy)
	Result    *models.DispatchResult `json:"result,omitempty"`  // structured result, for dispatch_complete
	CreatedAt time.Time             `json:"created_at"`
}

// pendingEntry is the docstore content for an in-flight dispatch record.
type pendingEntry struct {
	Agent     string    `json:"agent"`
	CreatedAt time.Time `json:"created_at"`
}

// AppendArg returns a [entroq.ModifyArg] that appends chunk to the log for
// sessionID. The arg can be composed with queue and other doc operations in a
// single atomic [entroq.EntroQ.Modify] call.
func AppendArg(sessionID string, chunk Chunk) entroq.ModifyArg {
	if chunk.CreatedAt.IsZero() {
		chunk.CreatedAt = time.Now()
	}
	return entroq.CreatingIn(nsLog,
		entroq.WithKeys(sessionID, secondaryKey()),
		entroq.WithContent(chunk),
	)
}

// PendingAddArg returns a [entroq.ModifyArg] that records childSessionID as an
// in-flight dispatch from sessionID. The child session ID is used as the
// document ID so it can be looked up directly via [ClaimPending].
func PendingAddArg(sessionID, childSessionID, agent string) entroq.ModifyArg {
	return entroq.CreatingIn(nsPending,
		entroq.WithIDKeys(childSessionID, sessionID, childSessionID),
		entroq.WithContent(pendingEntry{Agent: agent, CreatedAt: time.Now()}),
	)
}

// FindPending returns the pending dispatch record for childSessionID. The
// caller passes [entroq.DeletingDoc] on the returned doc as part of the
// atomic [entroq.EntroQ.Modify] that also appends the dispatch_complete chunk
// and re-enqueues the parent supervisor. The version embedded in the returned
// doc acts as an optimistic concurrency guard: if the entry was already deleted
// (double-delivery), the Modify fails with a DependencyError, which is correct.
//
// NOTE: for V1 serial dispatches this is sufficient. When parallel dispatches
// are added, consider whether a claim-before-delete approach is needed to
// prevent concurrent workers from racing on the same pending entry.
func FindPending(ctx context.Context, eq *entroq.EntroQ, childSessionID string) (*entroq.Doc, error) {
	docs, err := eq.Docs(ctx, entroq.DocsIn(nsPending).WithIDs(childSessionID))
	if err != nil {
		return nil, fmt.Errorf("sessionlog: find pending %s: %w", childSessionID, err)
	}
	if len(docs) == 0 {
		return nil, fmt.Errorf("sessionlog: pending dispatch %s not found", childSessionID)
	}
	return docs[0], nil
}

// PendingList returns the child session IDs of all in-flight dispatches for
// sessionID. An empty slice means all dispatches have resolved.
func PendingList(ctx context.Context, eq *entroq.EntroQ, sessionID string) ([]string, error) {
	docs, err := eq.Docs(ctx, &entroq.DocQuery{
		Namespace: nsPending,
		KeyStart:  sessionID,
		KeyEnd:    sessionID + "\x00",
	})
	if err != nil {
		return nil, fmt.Errorf("sessionlog: list pending %s: %w", sessionID, err)
	}
	ids := make([]string, len(docs))
	for i, doc := range docs {
		ids[i] = doc.SecondaryKey
	}
	return ids, nil
}

// Chunks returns all log chunks for sessionID in chronological order.
func Chunks(ctx context.Context, eq *entroq.EntroQ, sessionID string) ([]Chunk, error) {
	docs, err := eq.Docs(ctx, &entroq.DocQuery{
		Namespace: nsLog,
		KeyStart:  sessionID,
		KeyEnd:    sessionID + "\x00",
	})
	if err != nil {
		return nil, fmt.Errorf("sessionlog: read chunks %s: %w", sessionID, err)
	}
	chunks := make([]Chunk, 0, len(docs))
	for _, doc := range docs {
		c, err := entroq.GetContent[Chunk](doc)
		if err != nil {
			return nil, fmt.Errorf("sessionlog: unmarshal chunk: %w", err)
		}
		chunks = append(chunks, c)
	}
	return chunks, nil
}

// Transcript builds a message slice from the chunk log suitable for LLM input.
//
// User and assistant messages are replayed as structured content blocks.
// Dispatch events are reconstructed as tool_use/tool_result pairs using a
// synthetic ID ("dispatch_<childID>"), giving the LLM the correct semantic
// context: dispatch_pending becomes a tool_use in the preceding assistant turn,
// and dispatch_complete becomes a tool_result in a user message.
// Pending dispatch chunks (dispatch_pending without a matching complete) are
// omitted — still in flight.
func Transcript(ctx context.Context, eq *entroq.EntroQ, sessionID string) ([]models.Message, error) {
	chunks, err := Chunks(ctx, eq, sessionID)
	if err != nil {
		return nil, err
	}

	var msgs []models.Message
	var pendingAssistant models.ContentList

	flushAssistant := func() {
		if len(pendingAssistant) > 0 {
			msgs = append(msgs, models.Message{Role: "assistant", Content: pendingAssistant})
			pendingAssistant = nil
		}
	}

	for _, c := range chunks {
		switch c.Type {
		case ChunkUserMessage:
			flushAssistant()
			msgs = append(msgs, models.TextMessage("user", c.Content))

		case ChunkAssistantMessage:
			pendingAssistant = append(pendingAssistant, models.TextBlock(c.Content))

		case ChunkDispatchPending:
			// Accumulate tool_use block; don't flush yet — a single assistant turn
			// may contain multiple dispatches (fan-out), all belonging to the same
			// assistant message.
			pendingAssistant = append(pendingAssistant, models.ToolUseBlock(
				"dispatch_"+c.ChildID,
				"dispatch_to_agent",
				map[string]any{"agent": c.Agent},
			))

		case ChunkDispatchComplete:
			// End the assistant turn before the tool_result.
			flushAssistant()
			var content any
			if c.Result != nil {
				content = c.Result
			} else {
				content = fmt.Sprintf("[%s agent result]\n%s", c.Agent, c.Summary)
			}
			msgs = append(msgs, models.Message{
				Role:    "user",
				Content: models.ContentList{models.ToolResultBlock("dispatch_"+c.ChildID, content)},
			})
		}
	}

	flushAssistant()
	return msgs, nil
}

// CountDispatches returns the total number of dispatch_pending chunks in the
// session log. Used by the supervisor to enforce a per-session dispatch budget.
func CountDispatches(ctx context.Context, eq *entroq.EntroQ, sessionID string) (int, error) {
	chunks, err := Chunks(ctx, eq, sessionID)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, c := range chunks {
		if c.Type == ChunkDispatchPending {
			count++
		}
	}
	return count, nil
}

// secondaryKey returns a lexicographically ordered, collision-resistant key
// suitable for document ordering within a session. Format: 20-digit nanosecond
// timestamp padded to ensure sort order, plus 8 hex random digits for uniqueness.
func secondaryKey() string {
	return fmt.Sprintf("%020d-%08x", time.Now().UnixNano(), rand.Uint32())
}
