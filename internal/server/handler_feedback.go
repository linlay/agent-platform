package server

import (
	"errors"
	"net/http"
	"strings"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/chat"
)

func (s *Server) handleFeedback(w http.ResponseWriter, r *http.Request) {
	var req api.FeedbackRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	chatID := strings.TrimSpace(req.ChatID)
	runID := strings.TrimSpace(req.RunID)
	feedbackType := strings.TrimSpace(req.Type)
	if chatID == "" || runID == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "chatId and runId are required"))
		return
	}
	if feedbackType != "thumbs_down" && feedbackType != "clear" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "type must be thumbs_down or clear"))
		return
	}
	setAt, err := s.deps.Chats.SetFeedback(chatID, runID, feedbackType, req.Comment)
	if errors.Is(err, chat.ErrRunNotFound) {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "run not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(api.FeedbackResponse{
		ChatID: chatID,
		RunID:  runID,
		Type:   feedbackType,
		SetAt:  setAt,
	}))
}
