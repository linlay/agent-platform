package server

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
)

func (s *Server) handleFileHistory(w http.ResponseWriter, r *http.Request) {
	reader, ok := s.deps.Tools.(contracts.FileHistoryReader)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(http.StatusServiceUnavailable, "file history is not configured"))
		return
	}

	query := r.URL.Query()
	chatID := strings.TrimSpace(query.Get("chatId"))
	runID := strings.TrimSpace(query.Get("runId"))
	filePath := strings.TrimSpace(query.Get("filePath"))
	version := strings.TrimSpace(query.Get("version"))

	if runID == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "runId is required"))
		return
	}
	if filePath == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "filePath is required"))
		return
	}
	if version != "original" && version != "current" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "version must be original or current"))
		return
	}
	if chatID == "" && s.deps.Runs != nil {
		if status, ok := s.deps.Runs.RunStatus(runID); ok {
			chatID = strings.TrimSpace(status.ChatID)
		}
	}
	if !chat.ValidChatID(chatID) {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "chatId is required"))
		return
	}

	content, err := reader.ReadFileHistory(chatID, runID, filePath, version)
	if err != nil {
		switch {
		case errors.Is(err, os.ErrNotExist):
			writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "file history not found"))
		case errors.Is(err, os.ErrPermission):
			writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid file history request"))
		default:
			writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		}
		return
	}
	writeJSON(w, http.StatusOK, api.Success(api.FileHistoryResponse{Content: content}))
}
