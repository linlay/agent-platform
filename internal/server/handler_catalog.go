package server

import (
	"net/http"
	"strings"

	"agent-platform-runner-go/internal/api"
)

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	items, err := s.listAgentSummaries(r.URL.Query().Get("tag"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(items))
}

func (s *Server) handleAgent(w http.ResponseWriter, r *http.Request) {
	agentKey := strings.TrimSpace(r.URL.Query().Get("agentKey"))
	if agentKey == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "agentKey is required"))
		return
	}
	def, ok := s.deps.Registry.AgentDefinition(agentKey)
	if !ok {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "agent not found"))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(s.buildAgentDetailResponse(def)))
}

func (s *Server) handleTeams(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, api.Success(s.deps.Registry.Teams()))
}

func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, api.Success(s.deps.Registry.Skills(r.URL.Query().Get("tag"))))
}

func (s *Server) handleTools(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, api.Success(s.listTools(r.URL.Query().Get("kind"), r.URL.Query().Get("tag"))))
}

func (s *Server) handleTool(w http.ResponseWriter, r *http.Request) {
	toolName := r.URL.Query().Get("toolName")
	if toolName == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "toolName is required"))
		return
	}
	tool, ok := s.lookupTool(toolName)
	if !ok {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "tool not found"))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(tool))
}
