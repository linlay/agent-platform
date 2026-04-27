package server

import (
	"errors"
	"net/http"
	"strings"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/contracts"
)

func (s *Server) handleChatDelete(w http.ResponseWriter, r *http.Request) {
	var req api.DeleteChatRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	chatID := strings.TrimSpace(req.ChatID)
	if !chat.ValidChatID(chatID) {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid chatId"))
		return
	}
	if s.deps.Runs != nil {
		activeRun, ok, err := s.deps.Runs.ActiveRunForChat(chatID)
		var conflictErr *contracts.ActiveRunConflictError
		if errors.As(err, &conflictErr) {
			writeActiveRunConflict(w, conflictErr)
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
			return
		}
		if ok {
			writeJSON(w, http.StatusConflict, api.ApiResponse[map[string]any]{
				Code: http.StatusConflict,
				Msg:  "active_run_conflict",
				Data: map[string]any{
					"code":   "active_run_conflict",
					"chatId": chatID,
					"runIds": []string{activeRun.RunID},
				},
			})
			return
		}
	}
	if err := s.deps.Chats.DeleteChat(chatID); errors.Is(err, chat.ErrChatNotFound) {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "chat not found"))
		return
	} else if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(api.DeleteChatResponse{ChatID: chatID, Deleted: true}))
	s.broadcast("chat.deleted", map[string]any{"chatId": chatID})
}
