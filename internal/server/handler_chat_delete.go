package server

import (
	"errors"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
)

func (s *Server) handleChatDelete(w http.ResponseWriter, r *http.Request) {
	var req api.DeleteChatRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	chatID, err := queryOrBodyID(r, "chatId", req.ChatID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
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
			writeJSON(w, http.StatusConflict, api.ApiResponse[*api.ChatErrorInfo]{
				Code: http.StatusConflict,
				Msg:  activeRunConflictCode,
				Data: activeRunFoundInfo(chatID, []string{activeRun.RunID}),
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

func (s *Server) handleChatRename(w http.ResponseWriter, r *http.Request) {
	var req api.RenameChatRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	chatID, err := queryOrBodyID(r, "chatId", req.ChatID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
	chatName := strings.TrimSpace(req.ChatName)
	if !chat.ValidChatID(chatID) || chatName == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "chatId and chatName are required"))
		return
	}
	summary, err := s.deps.Chats.RenameChat(chatID, chatName)
	if errors.Is(err, chat.ErrChatNotFound) {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "chat not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	response := api.RenameChatResponse{ChatID: summary.ChatID, ChatName: summary.ChatName, Updated: true}
	writeJSON(w, http.StatusOK, api.Success(response))
	s.broadcast("chat.renamed", map[string]any{"chatId": summary.ChatID, "chatName": summary.ChatName, "agentKey": summary.AgentKey})
}
