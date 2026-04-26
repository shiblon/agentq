package api

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/agentq/pkg/workflow"
)

// sessionSummary is the shape returned by the list endpoint.
// Artifact content is excluded -- fetch the full session by ID to get it.
type sessionSummary struct {
	ID              string         `json:"id"`
	UserID          string         `json:"user_id"`
	Prompt          string         `json:"prompt"`
	ParentSessionID string         `json:"parent_session_id,omitempty"`
	Status          string         `json:"status"`
	ArtifactCount   int            `json:"artifact_count"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

func summarize(s *models.Session) sessionSummary {
	return sessionSummary{
		ID:              s.ID,
		UserID:          s.UserID,
		Prompt:          s.Prompt,
		ParentSessionID: s.ParentSessionID,
		Status:          s.Status,
		ArtifactCount:   len(s.Artifacts),
		CreatedAt:       s.CreatedAt,
		UpdatedAt:       s.UpdatedAt,
	}
}

// submitRequest is the body for POST /api/v1/sessions.
// UserID is ignored when authentication is enabled; the validated token's
// subject claim is used instead.
type submitRequest struct {
	Prompt       string `json:"prompt"`
	UserID       string `json:"user_id"`
	ContinueFrom string `json:"continue_from"`
	Repo         string `json:"repo"`
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

	summaries := make([]sessionSummary, 0, len(sessions))
	for _, s := range sessions {
		if statusFilter != "" && s.Status != statusFilter {
			continue
		}
		summaries = append(summaries, summarize(s))
	}

	writeJSON(w, http.StatusOK, summaries)
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
	// Prefer the validated token subject over the request body value.
	// Falls back to req.UserID for unauthenticated dev mode, then "api".
	if claims := ClaimsFromContext(r.Context()); claims != nil {
		req.UserID = claims.Subject
	} else if req.UserID == "" {
		req.UserID = "api"
	}

	result, err := workflow.SubmitSession(r.Context(), s.store, s.eq, workflow.SubmitRequest{
		UserID:           req.UserID,
		Prompt:           req.Prompt,
		ContinueFrom:     req.ContinueFrom,
		Repo:             req.Repo,
		HumanToken:       BearerToken(r),
		Compact:          req.Compact,
		ProvenanceIssuer: s.provenanceIssuer,
	})
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

func (s *Server) handleSessionsCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.UpdateSession(r.Context(), id, func(session *models.Session) error {
		switch session.Status {
		case "completed", "cancelled":
			return fmt.Errorf("session already %s", session.Status)
		}
		session.Status = "cancelled"
		return nil
	}); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("cancel session: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
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
