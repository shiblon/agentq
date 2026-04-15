package api

import (
	"fmt"
	"net/http"

	"github.com/shiblon/agentq/pkg/config"
)

func (s *Server) handleAgentsList(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Load(s.configFile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("load config: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, cfg.Agents)
}

func (s *Server) handleAgentsAdd(w http.ResponseWriter, r *http.Request) {
	var agent config.Agent
	if err := readJSON(r, &agent); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if agent.Name == "" || agent.Queue == "" || agent.Description == "" {
		writeError(w, http.StatusBadRequest, "name, queue, and description are required")
		return
	}

	cfg, err := config.Load(s.configFile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("load config: %v", err))
		return
	}
	if err := cfg.Add(agent); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err := cfg.Save(s.configFile); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("save config: %v", err))
		return
	}
	writeJSON(w, http.StatusCreated, agent)
}

func (s *Server) handleAgentsRemove(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	cfg, err := config.Load(s.configFile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("load config: %v", err))
		return
	}
	if err := cfg.Remove(name); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if err := cfg.Save(s.configFile); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("save config: %v", err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
