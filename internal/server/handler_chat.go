package server

import (
	"errors"
	"net/http"
	"strings"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/chat"
)

func (s *Server) handleChats(w http.ResponseWriter, r *http.Request) {
	items, err := s.deps.Chats.ListChats(r.URL.Query().Get("lastRunId"), r.URL.Query().Get("agentKey"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
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
			ReadStatus:     item.ReadStatus,
			ReadAt:         item.ReadAt,
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
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	chatID := r.URL.Query().Get("chatId")
	if chatID == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "chatId is required"))
		return
	}
	detail, err := s.deps.Chats.LoadChat(chatID)
	if errors.Is(err, chat.ErrChatNotFound) {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "chat not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	summary, err := s.deps.Chats.Summary(chatID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}

	s.enrichToolMetadata(detail.Events, summaryAgentKey(summary))

	includeRaw := strings.EqualFold(r.URL.Query().Get("includeRawMessages"), "true")
	response := api.ChatDetailResponse{
		ChatID:     detail.ChatID,
		ChatName:   detail.ChatName,
		Events:     detail.Events,
		References: nil,
	}
	if principal := PrincipalFromContext(r.Context()); principal != nil {
		response.ChatImageToken = s.ticketService.Issue(principal.Subject, detail.ChatID)
	}
	if includeRaw {
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
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) handleRead(w http.ResponseWriter, r *http.Request) {
	var req api.MarkChatReadRequest
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.ChatID) == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "chatId is required"))
		return
	}
	summary, err := s.deps.Chats.MarkRead(req.ChatID)
	if errors.Is(err, chat.ErrChatNotFound) {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "chat not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	readAt := int64(0)
	if summary.ReadAt != nil {
		readAt = *summary.ReadAt
	}
	writeJSON(w, http.StatusOK, api.Success(api.MarkChatReadResponse{
		ChatID:     summary.ChatID,
		ReadStatus: summary.ReadStatus,
		ReadAt:     readAt,
	}))
	s.broadcast("chat.read", map[string]any{
		"chatId":     summary.ChatID,
		"readStatus": summary.ReadStatus,
		"readAt":     readAt,
	})
}
