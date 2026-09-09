package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/ws"
)

const (
	chatOrderOperationSetMode   = "set_mode"
	chatOrderOperationMove      = "move"
	chatOrderOperationSetPinned = "set_pinned"
)

func (s *Server) chatOrderStore() (chat.OrderStore, error) {
	store, ok := s.deps.Chats.(chat.OrderStore)
	if !ok || store == nil {
		return nil, newAgentStatusError(http.StatusNotImplemented, "not_supported", "chat ordering is not supported")
	}
	return store, nil
}

func (s *Server) handleChatOrder(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		response, err := s.readChatOrder()
		s.writeAgentHTTPResponse(w, response, err)
	case http.MethodPut:
		var request api.UpdateChatOrderRequest
		if err := decodeJSON(r, &request); err != nil {
			s.writeAgentHTTPResponse(w, nil, newAgentStatusError(http.StatusBadRequest, "invalid_request", "invalid payload"))
			return
		}
		response, err := s.updateChatOrder(request)
		s.writeAgentHTTPResponse(w, response, err)
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPut)
		writeJSON(w, http.StatusMethodNotAllowed, api.Failure(http.StatusMethodNotAllowed, "method not allowed"))
	}
}

func (s *Server) readChatOrder() (api.ChatOrderResponse, error) {
	store, err := s.chatOrderStore()
	if err != nil {
		return api.ChatOrderResponse{}, err
	}
	state, err := store.ChatOrder()
	if err != nil {
		return api.ChatOrderResponse{}, err
	}
	return s.chatOrderResponseWithPins(state)
}

func (s *Server) updateChatOrder(request api.UpdateChatOrderRequest) (api.ChatOrderResponse, error) {
	store, err := s.chatOrderStore()
	if err != nil {
		return api.ChatOrderResponse{}, err
	}
	operation := strings.TrimSpace(request.Operation)
	var state chat.OrderState
	changed := true
	switch operation {
	case chatOrderOperationSetMode:
		if request.Pinned != nil || strings.TrimSpace(request.ChatID) != "" || strings.TrimSpace(request.BeforeChatID) != "" || strings.TrimSpace(request.AfterChatID) != "" {
			return api.ChatOrderResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "set_mode only accepts sortMode")
		}
		state, err = store.SetChatSortMode(chat.SortMode(strings.TrimSpace(request.SortMode)))
	case chatOrderOperationMove:
		if request.Pinned != nil || strings.TrimSpace(request.SortMode) != "" {
			return api.ChatOrderResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "move does not accept sortMode")
		}
		state, err = store.MoveChat(request.ChatID, request.BeforeChatID, request.AfterChatID)
	case chatOrderOperationSetPinned:
		if request.Pinned == nil || strings.TrimSpace(request.ChatID) == "" || request.SortMode != "" || request.BeforeChatID != "" || request.AfterChatID != "" {
			return api.ChatOrderResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "set_pinned requires chatId and boolean pinned only")
		}
		pinStore, ok := s.deps.Chats.(chat.PinnedStore)
		if !ok {
			return api.ChatOrderResponse{}, newAgentStatusError(http.StatusNotImplemented, "not_supported", "chat pinning is not supported")
		}
		_, changed, err = pinStore.SetChatPinned(request.ChatID, *request.Pinned)
		if err == nil {
			state, err = store.ChatOrder()
		}
	default:
		return api.ChatOrderResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "operation must be set_mode, move or set_pinned")
	}
	if err != nil {
		if errors.Is(err, chat.ErrChatNotFound) {
			return api.ChatOrderResponse{}, newAgentStatusError(http.StatusNotFound, "not_found", "chat not found")
		}
		var validationErr *chat.OrderValidationError
		if errors.As(err, &validationErr) {
			return api.ChatOrderResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", validationErr.Error())
		}
		return api.ChatOrderResponse{}, err
	}
	response, err := s.chatOrderResponseWithPins(state)
	if err == nil && changed {
		s.broadcast("chats.order.changed", map[string]any{"updatedAt": response.UpdatedAt})
	}
	return response, err
}

func (s *Server) chatOrderResponseWithPins(state chat.OrderState) (api.ChatOrderResponse, error) {
	response := chatOrderResponse(state)
	response.PinnedOrder = []string{}
	if store, ok := s.deps.Chats.(chat.PinnedStore); ok {
		pins, err := store.ChatPinned()
		if err != nil {
			return api.ChatOrderResponse{}, err
		}
		response.PinnedOrder = pins.Order
		if pins.UpdatedAt > state.UpdatedAt {
			response.UpdatedAt = &pins.UpdatedAt
		}
	}
	return response, nil
}

func chatOrderResponse(state chat.OrderState) api.ChatOrderResponse {
	response := api.ChatOrderResponse{SortMode: string(state.SortMode)}
	if state.UpdatedAt > 0 {
		updatedAt := state.UpdatedAt
		response.UpdatedAt = &updatedAt
	}
	return response
}

// An empty WebSocket payload is the GET equivalent. A payload with an
// operation is the PUT equivalent and uses the same discriminated request.
func (s *Server) wsChatOrder(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	request, err := ws.DecodePayload[api.UpdateChatOrderRequest](req)
	if err != nil {
		s.sendAgentWSResponse(conn, req, nil, newAgentStatusError(http.StatusBadRequest, "invalid_request", "invalid payload"))
		return
	}
	if strings.TrimSpace(request.Operation) == "" {
		if request.ChatID != "" || request.SortMode != "" || request.BeforeChatID != "" || request.AfterChatID != "" || request.Pinned != nil {
			s.sendAgentWSResponse(conn, req, nil, newAgentStatusError(http.StatusBadRequest, "invalid_request", "operation is required"))
			return
		}
		response, readErr := s.readChatOrder()
		s.sendAgentWSResponse(conn, req, response, readErr)
		return
	}
	response, updateErr := s.updateChatOrder(request)
	s.sendAgentWSResponse(conn, req, response, updateErr)
}
