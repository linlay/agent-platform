package server

import (
	"errors"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/timecontract"
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
		if isTimeContractViolation(err) {
			writeTimeContractViolation(w, err)
			return
		}
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	response, responseErr := feedbackResponse(chatID, runID, feedbackType, setAt)
	if responseErr != nil {
		writeTimeContractViolation(w, responseErr)
		return
	}
	writeJSON(w, http.StatusOK, api.Success(response))
}

func feedbackResponse(chatID, runID, feedbackType string, setAt int64) (api.FeedbackResponse, error) {
	optionalSetAt, err := timecontract.OptionalEpochMillis(setAt, "setAt", "feedback.response")
	if err != nil {
		return api.FeedbackResponse{}, err
	}
	return api.FeedbackResponse{
		ChatID: chatID,
		RunID:  runID,
		Type:   feedbackType,
		SetAt:  optionalSetAt,
	}, nil
}
