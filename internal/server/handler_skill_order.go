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

func (s *Server) handleSkillOrder(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		response, err := s.readSkillOrder(r.Context())
		s.writeAgentHTTPResponse(w, response, err)
	case http.MethodPut:
		var request api.UpdateSkillOrderRequest
		if err := decodeJSON(r, &request); err != nil {
			s.writeAgentHTTPResponse(w, nil, newAgentStatusError(http.StatusBadRequest, "invalid_request", "invalid payload"))
			return
		}
		response, err := s.updateSkillOrder(r.Context(), request)
		s.writeAgentHTTPResponse(w, response, err)
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPut)
		writeJSON(w, http.StatusMethodNotAllowed, api.Failure(http.StatusMethodNotAllowed, "method not allowed"))
	}
}

func (s *Server) readSkillOrder(ctx context.Context) (api.SkillOrderResponse, error) {
	user, err := s.catalogOrderUser(ctx)
	if err != nil {
		return api.SkillOrderResponse{}, err
	}
	state, err := s.skillOrder.Read(user)
	return skillOrderResponse(state), err
}

func (s *Server) updateSkillOrder(ctx context.Context, request api.UpdateSkillOrderRequest) (api.SkillOrderResponse, error) {
	user, err := s.catalogOrderUser(ctx)
	if err != nil {
		return api.SkillOrderResponse{}, err
	}
	key := strings.ToLower(strings.TrimSpace(request.Key))
	if key == "" || len(key) > 256 || strings.ContainsAny(key, "/\\\x00\r\n") || request.Pinned == nil {
		return api.SkillOrderResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "key and pinned are required")
	}
	if *request.Pinned && !s.knownPinnableSkill(key) {
		return api.SkillOrderResponse{}, newAgentStatusError(http.StatusNotFound, "skill_not_found", "skill is not available")
	}
	state, err := s.skillOrder.SetPinned(user, key, *request.Pinned)
	return skillOrderResponse(state), err
}

func (s *Server) knownPinnableSkill(key string) bool {
	if s.deps.Registry == nil || connector.IsReservedSkill(key) {
		return false
	}
	if registry, err := s.adminSkillRegistry(); err == nil {
		if _, found, err := registry.AdminSkill(key); err == nil && found {
			return true
		}
	}
	for _, skill := range s.deps.Registry.Skills("") {
		if strings.EqualFold(strings.TrimSpace(skill.Key), key) {
			return true
		}
	}
	for _, agent := range s.deps.Registry.Agents("all") {
		definition, ok := s.deps.Registry.AgentDefinition(agent.Key)
		if !ok || definition.IsConnectorSkill(key) {
			continue
		}
		for _, configured := range definition.Skills {
			if strings.EqualFold(strings.TrimSpace(configured), key) {
				return true
			}
		}
	}
	return false
}

func skillOrderResponse(state catalogorder.OrderState) api.SkillOrderResponse {
	response := api.SkillOrderResponse{Version: 1, Order: state.Order}
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
func (s *Server) wsSkillOrder(ctx context.Context, conn *ws.Conn, req ws.RequestFrame) {
	request, err := ws.DecodePayload[api.UpdateSkillOrderRequest](req)
	if err != nil {
		s.sendAgentWSResponse(conn, req, nil, newAgentStatusError(http.StatusBadRequest, "invalid_request", "invalid payload"))
		return
	}
	if request.Key == "" && request.Pinned == nil {
		response, readErr := s.readSkillOrder(ctx)
		s.sendAgentWSResponse(conn, req, response, readErr)
		return
	}
	response, updateErr := s.updateSkillOrder(ctx, request)
	s.sendAgentWSResponse(conn, req, response, updateErr)
}
