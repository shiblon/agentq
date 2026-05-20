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
	Type      ChunkType `json:"type"`
	Content   string    `json:"content,omitempty"`   // message text, for message types
	Agent     string    `json:"agent,omitempty"`     // agent name, for dispatch types
	ChildID   string    `json:"child_id,omitempty"`  // child session ID, for dispatch types
	Summary   string    `json:"summary,omitempty"`   // result summary, for dispatch_complete
	CreatedAt time.Time `json:"created_at"`
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
// Conversation chunks (user_message, assistant_message) are replayed verbatim.
// Completed dispatch chunks (dispatch_complete) are rendered as assistant
// messages so the result is attributed to the assistant's own action.
// Pending dispatch chunks (dispatch_pending) are omitted — still in flight.
//
// Adjacent messages of the same role are collapsed into one with a blank-line
// separator, preserving the alternating user/assistant structure required by
// the Claude API and CLI.
func Transcript(ctx context.Context, eq *entroq.EntroQ, sessionID string) ([]models.Message, error) {
	chunks, err := Chunks(ctx, eq, sessionID)
	if err != nil {
		return nil, err
	}
	var msgs []models.Message
	for _, c := range chunks {
		var role, content string
		switch c.Type {
		case ChunkUserMessage:
			role, content = "user", c.Content
		case ChunkAssistantMessage:
			role, content = "assistant", c.Content
		case ChunkDispatchComplete:
			role = "assistant"
			content = fmt.Sprintf("[%s agent result]\n%s", c.Agent, c.Summary)
		case ChunkDispatchPending:
			continue // omit: still in flight
		}
		if len(msgs) > 0 && msgs[len(msgs)-1].Role == role {
			msgs[len(msgs)-1].Content += "\n\n" + content
		} else {
			msgs = append(msgs, models.Message{Role: role, Content: content})
		}
	}
	return msgs, nil
}

// secondaryKey returns a lexicographically ordered, collision-resistant key
// suitable for document ordering within a session. Format: 20-digit nanosecond
// timestamp padded to ensure sort order, plus 8 hex random digits for uniqueness.
func secondaryKey() string {
	return fmt.Sprintf("%020d-%08x", time.Now().UnixNano(), rand.Uint32())
}
