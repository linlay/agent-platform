package server

import (
	"errors"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/memory"
)

func (s *Server) memorySystemEnabled() bool {
	return s != nil && s.deps.Config.Memory.Enabled
}

func (s *Server) memoryEnabledForAgent(agentDef catalog.AgentDefinition) bool {
	return s.memorySystemEnabled() && agentDef.MemoryEnabled
}

func (s *Server) memoryEnabledForAgentKey(agentKey string) bool {
	if !s.memorySystemEnabled() || s == nil || s.deps.Registry == nil {
		return false
	}
	def, ok := s.deps.Registry.AgentDefinition(strings.TrimSpace(agentKey))
	return ok && def.MemoryEnabled
}

func (s *Server) handleLearn(w http.ResponseWriter, r *http.Request) {
	var req api.LearnRequest
	if err := decodeJSON(r, &req); err != nil || req.ChatID == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "chatId is required"))
		return
	}
	if !s.memorySystemEnabled() {
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(http.StatusServiceUnavailable, "memory system is disabled"))
		return
	}
	if s.deps.Chats == nil {
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(http.StatusServiceUnavailable, "chat store is not configured"))
		return
	}
	if s.deps.Memory == nil {
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(http.StatusServiceUnavailable, "memory store is not configured"))
		return
	}
	summary, err := s.deps.Chats.Summary(req.ChatID)
	if errors.Is(err, chat.ErrChatNotFound) {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "chat not found"))
		return
	}
	if err != nil {
		if isTimeContractViolation(err) {
			writeTimeContractViolation(w, err)
			return
		}
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	if summary == nil {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "chat not found"))
		return
	}
	if !s.memoryEnabledForAgentKey(summary.AgentKey) {
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(http.StatusServiceUnavailable, "memory system is disabled"))
		return
	}
	trace, err := s.deps.Chats.LoadRunTrace(req.ChatID, summary.LastRunID)
	if errors.Is(err, chat.ErrChatNotFound) {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "chat not found"))
		return
	}
	if err != nil {
		if isTimeContractViolation(err) {
			writeTimeContractViolation(w, err)
			return
		}
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	principal := PrincipalFromContext(r.Context())
	userKey := "_local_default"
	if principal != nil && principal.Subject != "" {
		userKey = principal.Subject
	}
	response, err := s.deps.Memory.Learn(memory.LearnInput{
		Request:         req,
		Trace:           trace,
		AgentKey:        summary.AgentKey,
		TeamID:          summary.TeamID,
		UserKey:         userKey,
		SkillCandidates: s.deps.SkillCandidates,
	})
	if err != nil {
		if isTimeContractViolation(err) {
			writeTimeContractViolation(w, err)
			return
		}
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(response))
}
