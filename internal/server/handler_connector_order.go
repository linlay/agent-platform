package server

import (
	"context"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/catalogorder"
	"agent-platform/internal/connector"
	"agent-platform/internal/ws"
)

func (s *Server) handleConnectorOrder(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		response, err := s.readConnectorOrder(r.Context())
		s.writeAgentHTTPResponse(w, response, err)
	case http.MethodPut:
		var request api.UpdateConnectorOrderRequest
		if err := decodeJSON(r, &request); err != nil {
			s.writeAgentHTTPResponse(w, nil, newAgentStatusError(http.StatusBadRequest, "invalid_request", "invalid payload"))
			return
		}
		response, err := s.updateConnectorOrder(r.Context(), request)
		s.writeAgentHTTPResponse(w, response, err)
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPut)
		writeJSON(w, http.StatusMethodNotAllowed, api.Failure(http.StatusMethodNotAllowed, "method not allowed"))
	}
}

func (s *Server) readConnectorOrder(ctx context.Context) (api.ConnectorOrderResponse, error) {
	user, err := s.catalogOrderUser(ctx)
	if err != nil {
		return api.ConnectorOrderResponse{}, err
	}
	state, err := s.connectorOrder.Read(user)
	return connectorOrderResponse(state), err
}

func (s *Server) updateConnectorOrder(ctx context.Context, request api.UpdateConnectorOrderRequest) (api.ConnectorOrderResponse, error) {
	user, err := s.catalogOrderUser(ctx)
	if err != nil {
		return api.ConnectorOrderResponse{}, err
	}
	key := strings.ToLower(strings.TrimSpace(request.Key))
	if !connector.ValidID(key) || len(key) > 256 || request.Pinned == nil {
		return api.ConnectorOrderResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "key and pinned are required")
	}
	if *request.Pinned && !s.knownPinnableConnector(key) {
		return api.ConnectorOrderResponse{}, newAgentStatusError(http.StatusNotFound, "connector_not_found", "connector is not available")
	}
	state, err := s.connectorOrder.SetPinned(user, key, *request.Pinned)
	return connectorOrderResponse(state), err
}

func (s *Server) knownPinnableConnector(key string) bool {
	if !connector.ValidID(key) {
		return false
	}
	_, err := s.connectorSources().Load(key)
	return err == nil
}

func connectorOrderResponse(state catalogorder.OrderState) api.ConnectorOrderResponse {
	response := api.ConnectorOrderResponse{Version: 1, Order: state.Order}
	if response.Order == nil {
		response.Order = []string{}
	}
	if state.UpdatedAt > 0 {
		response.UpdatedAt = &state.UpdatedAt
	}
	return response
}

// Empty payload reads; key+pinned sets the explicit state. Identity always
// comes from the authenticated connection, never a client-supplied user key.
func (s *Server) wsConnectorOrder(ctx context.Context, conn *ws.Conn, req ws.RequestFrame) {
	request, err := ws.DecodePayload[api.UpdateConnectorOrderRequest](req)
	if err != nil {
		s.sendAgentWSResponse(conn, req, nil, newAgentStatusError(http.StatusBadRequest, "invalid_request", "invalid payload"))
		return
	}
	if request.Key == "" && request.Pinned == nil {
		response, readErr := s.readConnectorOrder(ctx)
		s.sendAgentWSResponse(conn, req, response, readErr)
		return
	}
	response, updateErr := s.updateConnectorOrder(ctx, request)
	s.sendAgentWSResponse(conn, req, response, updateErr)
}
