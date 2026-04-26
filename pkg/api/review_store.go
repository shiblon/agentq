package api

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/entroq"
)

// reviewStore pre-claims tasks from the human_review queue and holds them so
// that the approve/reject HTTP handlers can act on a specific task by ID.
// Tasks are claimed for claimDuration; if the server restarts before they are
// acted on they return to the queue automatically.
type reviewStore struct {
	eq            *entroq.EntroQ
	claimDuration time.Duration

	mu   sync.Mutex
	held map[string]*heldReview // task ID -> claim
}

type heldReview struct {
	task    *entroq.Task
	request models.HumanReviewRequest
}

func newReviewStore(eq *entroq.EntroQ) *reviewStore {
	return &reviewStore{
		eq:            eq,
		claimDuration: 30 * time.Minute,
		held:          make(map[string]*heldReview),
	}
}

// drain claims all immediately-available tasks from human_review and stores
// them. Already-held tasks are unaffected.
func (rs *reviewStore) drain(ctx context.Context) {
	for {
		task, err := rs.eq.TryClaim(ctx,
			entroq.From("human_review"),
			entroq.ClaimFor(rs.claimDuration),
		)
		if err != nil || task == nil {
			return
		}
		var req models.HumanReviewRequest
		if err := json.Unmarshal(task.Value, &req); err != nil {
			rs.eq.Modify(ctx, task.Delete()) //nolint:errcheck -- discard malformed
			continue
		}
		rs.mu.Lock()
		rs.held[task.ID] = &heldReview{task: task, request: req}
		rs.mu.Unlock()
	}
}

// list drains any new tasks then returns all currently held review items.
func (rs *reviewStore) list(ctx context.Context) []reviewItem {
	rs.drain(ctx)
	rs.mu.Lock()
	defer rs.mu.Unlock()
	items := make([]reviewItem, 0, len(rs.held))
	for _, h := range rs.held {
		items = append(items, reviewItem{
			TaskID:  h.task.ID,
			At:      h.task.At.Format("2006-01-02T15:04:05Z07:00"),
			Request: h.request,
		})
	}
	return items
}

// process atomically deletes the held task and posts a HumanReviewReply to
// the request's reply queue. Returns an error if taskID is not held.
func (rs *reviewStore) process(ctx context.Context, taskID, outcome, humanInput string) error {
	rs.mu.Lock()
	h, ok := rs.held[taskID]
	if !ok {
		rs.mu.Unlock()
		return fmt.Errorf("review task %s not found (may have already been handled)", taskID)
	}
	delete(rs.held, taskID)
	rs.mu.Unlock()

	reply := models.NewReviewReply(h.request.SessionURI, outcome, humanInput, taskID)
	reply.ProvenanceToken = h.request.ProvenanceToken
	replyBytes, err := json.Marshal(reply)
	if err != nil {
		return fmt.Errorf("marshal reply: %w", err)
	}
	if _, err := rs.eq.Modify(ctx,
		h.task.Delete(),
		entroq.InsertingInto(h.request.ReplyQueue, entroq.WithRawValue(replyBytes)),
	); err != nil {
		// Put it back so a retry is possible.
		rs.mu.Lock()
		rs.held[taskID] = h
		rs.mu.Unlock()
		return fmt.Errorf("post review reply: %w", err)
	}
	return nil
}
