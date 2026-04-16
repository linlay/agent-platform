package server

import (
	"errors"
	"net/http"
	"strings"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/chat"
)

func (s *Server) executeRemember(req api.RememberRequest) (api.RememberResponse, error) {
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
	writeJSON(w, http.StatusOK, api.Success(api.LearnResponse{
		Accepted:  false,
		Status:    "not_connected",
		RequestID: req.RequestID,
		ChatID:    req.ChatID,
	}))
}
