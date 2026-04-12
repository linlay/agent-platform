package server

import (
	"errors"
	"net/http"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/chat"
)

func (s *Server) handleRemember(w http.ResponseWriter, r *http.Request) {
	var req api.RememberRequest
	if err := decodeJSON(r, &req); err != nil || req.RequestID == "" || req.ChatID == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "requestId and chatId are required"))
		return
	}
	detail, err := s.deps.Chats.LoadChat(req.ChatID)
	if errors.Is(err, chat.ErrChatNotFound) {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "chat not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	items, err := s.deps.Chats.ListChats("", "")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	agentKey := ""
	for _, item := range items {
		if item.ChatID == req.ChatID {
			agentKey = item.AgentKey
			break
		}
	}
	response, err := s.deps.Memory.Remember(detail, req, agentKey)
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
