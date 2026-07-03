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

func (s *Server) handleChatDerive(w http.ResponseWriter, r *http.Request) {
	var req api.DeriveChatRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	response, statusErr := s.deriveChat(req)
	if statusErr != nil {
		writeStatusError(w, statusErr)
		return
	}
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) deriveChat(req api.DeriveChatRequest) (api.DeriveChatResponse, *statusError) {
	sourceChatID := strings.TrimSpace(req.SourceChatID)
	sourceRunID := strings.TrimSpace(req.SourceRunID)
	targetChatID := strings.TrimSpace(req.ChatID)
	if !chat.ValidChatID(sourceChatID) {
		return api.DeriveChatResponse{}, &statusError{status: http.StatusBadRequest, code: "invalid_request", message: "sourceChatId is required"}
	}
	if targetChatID == "" {
		targetChatID = newChatID()
	} else if !chat.ValidChatID(targetChatID) {
		return api.DeriveChatResponse{}, &statusError{status: http.StatusBadRequest, code: "invalid_request", message: "invalid chatId"}
	}
	if s.deps.Runs != nil {
		activeRun, ok, err := s.deps.Runs.ActiveRunForChat(sourceChatID)
		var conflictErr *contracts.ActiveRunConflictError
		if errors.As(err, &conflictErr) {
			return api.DeriveChatResponse{}, &statusError{
				status:  http.StatusConflict,
				code:    activeRunConflictCode,
				message: activeRunConflictMessage,
				data:    activeRunConflictInfo(conflictErr),
			}
		}
		if err != nil {
			return api.DeriveChatResponse{}, &statusError{status: http.StatusInternalServerError, code: "internal_error", message: err.Error()}
		}
		if ok {
			return api.DeriveChatResponse{}, &statusError{
				status:  http.StatusConflict,
				code:    activeRunConflictCode,
				message: activeRunFoundMessage,
				data:    activeRunFoundInfo(sourceChatID, []string{activeRun.RunID}),
			}
		}
	}
	result, err := s.deps.Chats.DeriveChat(chat.DeriveChatRequest{
		SourceChatID: sourceChatID,
		SourceRunID:  sourceRunID,
		ChatID:       targetChatID,
		ChatName:     req.ChatName,
	})
	switch {
	case err == nil:
	case errors.Is(err, os.ErrPermission):
		return api.DeriveChatResponse{}, &statusError{status: http.StatusBadRequest, code: "invalid_request", message: "invalid chatId"}
	case errors.Is(err, chat.ErrChatNotFound):
		return api.DeriveChatResponse{}, &statusError{status: http.StatusNotFound, code: "not_found", message: "source chat not found"}
	case errors.Is(err, chat.ErrRunNotFound):
		return api.DeriveChatResponse{}, &statusError{status: http.StatusNotFound, code: "not_found", message: "source run not found"}
	case errors.Is(err, chat.ErrChatAlreadyActive):
		return api.DeriveChatResponse{}, &statusError{status: http.StatusConflict, code: "chat_exists", message: "target chat already exists"}
	case errors.Is(err, chat.ErrChatPendingAwaiting):
		return api.DeriveChatResponse{}, &statusError{status: http.StatusConflict, code: awaitingPendingCode, message: awaitingPendingMessage}
	case errors.Is(err, chat.ErrRunIncomplete):
		return api.DeriveChatResponse{}, &statusError{status: http.StatusConflict, code: "run_incomplete", message: "source run is not complete"}
	default:
		return api.DeriveChatResponse{}, &statusError{status: http.StatusInternalServerError, code: "internal_error", message: err.Error()}
	}
	response := mapDeriveChatResponse(result)
	s.broadcast("chat.created", map[string]any{
		"chatId":    response.ChatID,
		"chatName":  response.ChatName,
		"agentKey":  response.AgentKey,
		"timestamp": response.CreatedAt,
	})
	s.broadcast("chat.updated", map[string]any{
		"chatId":         response.ChatID,
		"lastRunId":      response.LastRunID,
		"lastRunContent": result.Summary.LastRunContent,
		"updatedAt":      response.UpdatedAt,
	})
	return response, nil
}

func mapDeriveChatResponse(result chat.DeriveChatResult) api.DeriveChatResponse {
	return api.DeriveChatResponse{
		ChatID:       result.Summary.ChatID,
		ChatName:     result.Summary.ChatName,
		AgentKey:     result.Summary.AgentKey,
		TeamID:       result.Summary.TeamID,
		SourceChatID: result.SourceChatID,
		SourceRunID:  result.SourceRunID,
		LastRunID:    result.LastRunID,
		CopiedRuns:   result.CopiedRuns,
		CreatedAt:    result.Summary.CreatedAt,
		UpdatedAt:    result.Summary.UpdatedAt,
	}
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
