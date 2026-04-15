package api

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/agentq/pkg/workflow"
)

// submitRequest is the body for POST /api/v1/sessions.
type submitRequest struct {
	Prompt       string `json:"prompt"`
	UserID       string `json:"user_id"`
	ContinueFrom string `json:"continue_from"`
	Compact      bool   `json:"compact"`
}

// submitResponse is the body returned on successful session creation.
type submitResponse struct {
	SessionID  string `json:"session_id"`
	SessionURI string `json:"session_uri"`
	Status     string `json:"status"`
}

func (s *Server) handleSessionsList(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	limit := 50
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			limit = n
		}
	}
	statusFilter := r.URL.Query().Get("status")

	sessions, err := s.store.ListSessions(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("list sessions: %v", err))
		return
	}

	if statusFilter != "" {
		filtered := sessions[:0]
		for _, s := range sessions {
			if s.Status == statusFilter {
				filtered = append(filtered, s)
			}
		}
		sessions = filtered
	}

	writeJSON(w, http.StatusOK, sessions)
}

func (s *Server) handleSessionsSubmit(w http.ResponseWriter, r *http.Request) {
	var req submitRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Prompt == "" {
		writeError(w, http.StatusBadRequest, "prompt is required")
		return
	}
	if req.UserID == "" {
		req.UserID = "api"
	}

	result, err := workflow.SubmitSession(r.Context(), s.store, s.eq, req.UserID, req.Prompt, req.ContinueFrom, req.Compact)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("submit session: %v", err))
		return
	}

	writeJSON(w, http.StatusCreated, submitResponse{
		SessionID:  result.SessionID,
		SessionURI: result.SessionURI,
		Status:     "pending",
	})
}

func (s *Server) handleSessionsGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	session, err := s.store.GetSession(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("session %s not found", id))
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) handleSessionsChain(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var chain []*models.Session
	seen := map[string]bool{}
	for id != "" && !seen[id] {
		seen[id] = true
		session, err := s.store.GetSession(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusNotFound, fmt.Sprintf("session %s not found", id))
			return
		}
		chain = append([]*models.Session{session}, chain...) // prepend: oldest first
		id = session.ParentSessionID
	}

	writeJSON(w, http.StatusOK, chain)
}

func (s *Server) handleSessionsResult(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	session, err := s.store.GetSession(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("session %s not found", id))
		return
	}

	switch session.Status {
	case "pending", "in_progress":
		writeError(w, http.StatusConflict, fmt.Sprintf("session is still running (status: %s)", session.Status))
		return
	case "awaiting_review":
		writeError(w, http.StatusConflict, "session is awaiting human review")
		return
	}

	// Find the last fresh specialist artifact.
	var last *models.Artifact
	for i := len(session.Artifacts) - 1; i >= 0; i-- {
		a := session.Artifacts[i]
		if a.AgentName == "supervisor" {
			continue
		}
		if a.OriginSessionID == "" {
			last = &session.Artifacts[i]
			break
		}
	}
	// Fall back to last inherited artifact if no fresh work.
	if last == nil {
		for i := len(session.Artifacts) - 1; i >= 0; i-- {
			if session.Artifacts[i].AgentName != "supervisor" {
				last = &session.Artifacts[i]
				break
			}
		}
	}

	if last == nil {
		writeJSON(w, http.StatusOK, map[string]string{"content": ""})
		return
	}
	writeJSON(w, http.StatusOK, last)
}
