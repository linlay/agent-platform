package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/contracts"
)

func (s *Server) listChatSummaries(lastRunID string, agentKey string) ([]api.ChatSummaryResponse, error) {
	items, err := s.deps.Chats.ListChats(lastRunID, agentKey)
	if err != nil {
		return nil, err
	}
	response := make([]api.ChatSummaryResponse, 0, len(items))
	for _, item := range items {
		resp := api.ChatSummaryResponse{
			ChatID:         item.ChatID,
			ChatName:       item.ChatName,
			AgentKey:       item.AgentKey,
			TeamID:         item.TeamID,
			CreatedAt:      item.CreatedAt,
			UpdatedAt:      item.UpdatedAt,
			LastRunID:      item.LastRunID,
			LastRunContent: item.LastRunContent,
			Read:           toAPIReadState(item.Read),
		}
		if item.PendingAwaiting != nil {
			resp.PendingAwaiting = &api.PendingAwaiting{
				AwaitingID: item.PendingAwaiting.AwaitingID,
				RunID:      item.PendingAwaiting.RunID,
				Mode:       item.PendingAwaiting.Mode,
				CreatedAt:  item.PendingAwaiting.CreatedAt,
			}
		}
		if item.Usage != nil && item.Usage.TotalTokens > 0 {
			resp.Usage = &api.ChatUsageData{
				PromptTokens:     item.Usage.PromptTokens,
				CompletionTokens: item.Usage.CompletionTokens,
				TotalTokens:      item.Usage.TotalTokens,
			}
		}
		response = append(response, resp)
	}
	return response, nil
}

func (s *Server) loadChatDetail(ctx context.Context, chatID string, includeRawMessages bool) (api.ChatDetailResponse, error) {
	detail, err := s.deps.Chats.LoadChat(chatID)
	if err != nil {
		return api.ChatDetailResponse{}, err
	}
	summary, err := s.deps.Chats.Summary(chatID)
	if err != nil {
		return api.ChatDetailResponse{}, err
	}

	s.enrichToolMetadata(detail.Events, summaryAgentKey(summary))

	response := api.ChatDetailResponse{
		ChatID:     detail.ChatID,
		ChatName:   detail.ChatName,
		Events:     detail.Events,
		References: nil,
	}
	if principal := PrincipalFromContext(ctx); principal != nil {
		response.ChatImageToken = s.ticketService.Issue(principal.Subject, detail.ChatID)
	}
	if includeRawMessages {
		response.RawMessages = detail.RawMessages
	}
	if detail.Plan != nil {
		response.Plan = detail.Plan
	}
	if detail.Artifact != nil {
		response.Artifact = detail.Artifact
	}
	if summary != nil && summary.Usage != nil && summary.Usage.TotalTokens > 0 {
		response.Usage = &api.ChatUsageData{
			PromptTokens:     summary.Usage.PromptTokens,
			CompletionTokens: summary.Usage.CompletionTokens,
			TotalTokens:      summary.Usage.TotalTokens,
		}
	}
	if s.deps.Runs != nil {
		activeRun, ok, activeErr := s.deps.Runs.ActiveRunForChat(chatID)
		if activeErr != nil {
			return api.ChatDetailResponse{}, activeErr
		}
		if ok {
			response.ActiveRun = &api.ActiveRunInfo{
				RunID:     activeRun.RunID,
				State:     string(activeRun.State),
				LastSeq:   activeRun.LastSeq,
				OldestSeq: activeRun.OldestSeq,
				StartedAt: activeRun.StartedAt,
			}
		}
	}
	return response, nil
}

func writeActiveRunConflict(w http.ResponseWriter, conflict *contracts.ActiveRunConflictError) {
	writeJSON(w, http.StatusConflict, api.ApiResponse[map[string]any]{
		Code: http.StatusConflict,
		Msg:  "active_run_conflict",
		Data: map[string]any{
			"code":   "active_run_conflict",
			"chatId": conflict.ChatID,
			"runIds": append([]string(nil), conflict.RunIDs...),
		},
	})
}

func (s *Server) handleChats(w http.ResponseWriter, r *http.Request) {
	response, err := s.listChatSummaries(r.URL.Query().Get("lastRunId"), r.URL.Query().Get("agentKey"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	chatID := r.URL.Query().Get("chatId")
	if chatID == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "chatId is required"))
		return
	}
	response, err := s.loadChatDetail(r.Context(), chatID, strings.EqualFold(r.URL.Query().Get("includeRawMessages"), "true"))
	if errors.Is(err, chat.ErrChatNotFound) {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "chat not found"))
		return
	}
	var conflictErr *contracts.ActiveRunConflictError
	if errors.As(err, &conflictErr) {
		writeActiveRunConflict(w, conflictErr)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) handleRead(w http.ResponseWriter, r *http.Request) {
	var req api.MarkChatReadRequest
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.ChatID) == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "chatId is required"))
		return
	}
	summary, err := s.deps.Chats.MarkRead(req.ChatID, req.RunID)
	if errors.Is(err, chat.ErrChatNotFound) {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "chat not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	agentUnreadCount, err := s.agentUnreadCount(summary.AgentKey)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(s.buildMarkReadResponse(summary, agentUnreadCount)))
	s.broadcastChatReadState("chat.read", summary, agentUnreadCount)
}
