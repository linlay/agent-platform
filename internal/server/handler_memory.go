package server

import (
	"errors"
	"net/http"
	"strings"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/memory"
)

func (s *Server) memorySystemEnabled() bool {
	return s != nil && s.deps.Config.Memory.AutoRememberEnabled
}

func (s *Server) executeRemember(req api.RememberRequest) (api.RememberResponse, error) {
	if !s.memorySystemEnabled() {
		return api.RememberResponse{}, errors.New("memory system is disabled")
	}
	detail, err := s.deps.Chats.LoadChat(req.ChatID)
	if err != nil {
		return api.RememberResponse{}, err
	}
	items, err := s.deps.Chats.ListChats("", "")
	if err != nil {
		return api.RememberResponse{}, err
	}
	agentKey := ""
	for _, item := range items {
		if item.ChatID == req.ChatID {
			agentKey = item.AgentKey
			break
		}
	}
	return s.deps.Memory.Remember(detail, req, agentKey)
}

func (s *Server) handleRemember(w http.ResponseWriter, r *http.Request) {
	var req api.RememberRequest
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.RequestID) == "" || strings.TrimSpace(req.ChatID) == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "requestId and chatId are required"))
		return
	}
	if !s.memorySystemEnabled() {
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(http.StatusServiceUnavailable, "memory system is disabled"))
		return
	}
	response, err := s.executeRemember(req)
	if errors.Is(err, chat.ErrChatNotFound) {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "chat not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(response))
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
	if errors.Is(err, chat.ErrChatNotFound) || summary == nil {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "chat not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	trace, err := s.deps.Chats.LoadRunTrace(req.ChatID, summary.LastRunID)
	if errors.Is(err, chat.ErrChatNotFound) {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "chat not found"))
		return
	}
	if err != nil {
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
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(response))
}
