package api

import (
	"fmt"
	"net/http"

	"github.com/shiblon/agentq/pkg/models"
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
	items := s.reviews.list(r.Context())
	if items == nil {
		items = []reviewItem{}
	}
	writeJSON(w, http.StatusOK, items)
}

type reviewActionRequest struct {
	HumanInput string `json:"human_input"`
}

func (s *Server) handleReviewApprove(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("task_id")
	var body reviewActionRequest
	_ = readJSON(r, &body) // input is optional
	if err := s.reviews.process(r.Context(), taskID, "approved", body.HumanInput); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"outcome": "approved"})
}

func (s *Server) handleReviewReject(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("task_id")
	var body reviewActionRequest
	_ = readJSON(r, &body) // input is optional
	if err := s.reviews.process(r.Context(), taskID, "rejected", body.HumanInput); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"outcome": "rejected"})
}
