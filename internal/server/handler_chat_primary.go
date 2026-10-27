package server

import (
	"context"
	"errors"
	"net/http"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/conversation"
	"agent-platform/internal/ws"
)

func (s *Server) handleChatPrimary(w http.ResponseWriter, r *http.Request) {
	var request api.SetPrimaryChatRequest
	if err := decodeJSON(r, &request); err != nil {
		s.writeAgentHTTPResponse(w, nil, newAgentStatusError(http.StatusBadRequest, "invalid_request", "invalid payload"))
		return
	}
	response, err := s.setChatPrimary(request)
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) wsChatPrimary(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	request, err := ws.DecodePayload[api.SetPrimaryChatRequest](req)
	if err != nil {
		s.sendAgentWSResponse(conn, req, nil, newAgentStatusError(http.StatusBadRequest, "invalid_request", "invalid payload"))
		return
	}
	response, setErr := s.setChatPrimary(request)
	s.sendAgentWSResponse(conn, req, response, setErr)
}

func (s *Server) setChatPrimary(request api.SetPrimaryChatRequest) (conversation.PrimaryResult, error) {
	result, err := s.conversationService().SetChatPrimary(request.ChatID)
	if err == nil {
		return result, nil
	}
	var busy *chat.PrimaryChatBusyError
	var validationErr *chat.OrderValidationError
	switch {
	case errors.As(err, &busy):
		return result, newAgentStatusErrorWithData(http.StatusConflict, "primary_chat_busy", busy.Error(), map[string]any{
			"agentKey": busy.AgentKey, "chatId": busy.ChatID, "runIds": busy.RunIDs,
		})
	case errors.Is(err, chat.ErrChatNotFound):
		return result, newAgentStatusError(http.StatusNotFound, "not_found", "chat not found")
	case errors.As(err, &validationErr):
		return result, newAgentStatusError(http.StatusBadRequest, "invalid_request", validationErr.Error())
	case errors.Is(err, conversation.ErrPrimaryNotSupported):
		return result, newAgentStatusError(http.StatusNotImplemented, "not_supported", "primary chat is not supported")
	}
	return result, err
}

// primaryChatsByAgent reads every primary Chat in one list query, keyed by
// owning Agent, with the same active-run projection as navigation lists.
func (s *Server) primaryChatsByAgent() (map[string]api.ChatSummaryResponse, error) {
	if _, ok := s.deps.Chats.(chat.PrimaryStore); !ok {
		return nil, nil
	}
	primary := true
	items, err := s.conversationService().ListSummariesWithOptions(chat.ListOptions{Primary: &primary})
	if err != nil {
		return nil, err
	}
	summaries, err := s.mapChatSummariesWithActiveRuns(items)
	if err != nil {
		return nil, err
	}
	byAgent := make(map[string]api.ChatSummaryResponse, len(summaries))
	for _, summary := range summaries {
		byAgent[summary.AgentKey] = summary
	}
	return byAgent, nil
}
