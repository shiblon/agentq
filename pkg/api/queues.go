package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/entroq"
)

// queueInfo is returned by GET /api/v1/queues.
type queueInfo struct {
	Name    string `json:"name"`
	Pending int    `json:"pending"` // tasks available to claim
	Total   int    `json:"total"`   // all tasks including claimed/delayed
}

func (s *Server) handleQueuesList(w http.ResponseWriter, r *http.Request) {
	stats, err := s.eq.QueueStats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("queue stats: %v", err))
		return
	}

	var queues []queueInfo
	for name, stat := range stats {
		queues = append(queues, queueInfo{
			Name:    name,
			Pending: stat.Available,
			Total:   stat.Size,
		})
	}
	writeJSON(w, http.StatusOK, queues)
}

// reviewItem is a pending human-review task with its parsed request.
type reviewItem struct {
	TaskID  string                    `json:"task_id"`
	At      string                    `json:"at"` // arrival time (RFC3339)
	Request models.HumanReviewRequest `json:"request"`
}

func (s *Server) handleReviewList(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.eq.Tasks(r.Context(), "human_review")
	if err != nil {
		// human_review queue may not exist yet; return empty list.
		if entroq.IsCanceled(err) || len(tasks) == 0 && err != nil {
			writeJSON(w, http.StatusOK, []reviewItem{})
			return
		}
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("list review tasks: %v", err))
		return
	}

	items := make([]reviewItem, 0, len(tasks))
	for _, t := range tasks {
		var req models.HumanReviewRequest
		if err := json.Unmarshal(t.Value, &req); err != nil {
			continue // skip malformed tasks
		}
		items = append(items, reviewItem{
			TaskID:  t.ID,
			At:      t.At.Format("2006-01-02T15:04:05Z07:00"),
			Request: req,
		})
	}
	writeJSON(w, http.StatusOK, items)
}
