package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/catalogorder"
	"agent-platform/internal/connector"
	"agent-platform/internal/ws"
)

func (s *Server) handleAgentSkills(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		response, err := s.listSkillsForAgent(r.Context(), r.URL.Query().Get("agentKey"))
		s.writeAgentHTTPResponse(w, response, err)
	case http.MethodPut:
		var request api.UpdateAgentSkillPinRequest
		if err := decodeJSON(r, &request); err != nil {
			s.writeAgentHTTPResponse(w, nil, newAgentStatusError(http.StatusBadRequest, "invalid_request", "invalid payload"))
			return
		}
		response, err := s.updateAgentSkillPin(r.Context(), request)
		s.writeAgentHTTPResponse(w, response, err)
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeJSON(w, http.StatusMethodNotAllowed, api.Failure(http.StatusMethodNotAllowed, "method not allowed"))
	}
}

func (s *Server) wsAgentSkills(ctx context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[struct {
		AgentKey string          `json:"agentKey"`
		ID       json.RawMessage `json:"id"`
		Pinned   json.RawMessage `json:"pinned"`
	}](req)
	if err != nil {
		s.sendAgentWSError(conn, req, agentSkillsStatusError(http.StatusBadRequest, "invalid_request", "invalid payload"))
		return
	}
	locale := conn.Locale()
	respond := func(response api.AgentSkillsResponse, err error) {
		if err != nil {
			s.sendAgentWSError(conn, req, err)
			return
		}
		conn.SendResponse(req.Type, req.ID, 0, "success", localizeSkillResponse(locale, response))
		conn.CompleteRequest(req.ID)
	}
	// Any mutation field denotes a write; incomplete writes must fail validation.
	if payload.ID != nil || payload.Pinned != nil {
		request, err := ws.DecodePayload[api.UpdateAgentSkillPinRequest](req)
		if err != nil {
			s.sendAgentWSError(conn, req, agentSkillsStatusError(http.StatusBadRequest, "invalid_request", "invalid payload"))
			return
		}
		response, err := s.updateAgentSkillPin(ctx, request)
		respond(response, err)
		return
	}
	response, err := s.listSkillsForAgent(ctx, payload.AgentKey)
	respond(response, err)
}

func (s *Server) listSkillsForAgent(ctx context.Context, agentKey string) (api.AgentSkillsResponse, error) {
	user, err := s.catalogOrderUser(ctx)
	if err != nil {
		return api.AgentSkillsResponse{}, err
	}
	state, err := s.skillOrder.Read(user)
	if err != nil {
		return api.AgentSkillsResponse{}, err
	}
	response := skillPinsResponse(state)
	agentKey = strings.TrimSpace(agentKey)
	if s.deps.Registry == nil {
		return api.AgentSkillsResponse{}, agentSkillsStatusError(http.StatusServiceUnavailable, "skill_catalog_unavailable", "skill catalog is not configured")
	}
	configured := map[string]bool{}
	if agentKey != "" {
		definition, ok := s.deps.Registry.AgentDefinition(agentKey)
		if !ok {
			return api.AgentSkillsResponse{}, agentSkillsStatusError(http.StatusNotFound, "agent_not_found", "agent not found")
		}
		response.AgentKey = definition.Key
		for _, key := range definition.Skills {
			if !definition.IsConnectorSkill(key) && !connector.IsReservedSkill(key) {
				configured[strings.ToLower(strings.TrimSpace(key))] = true
			}
		}
	}
	seen := map[string]bool{}
	for _, skill := range s.deps.Registry.Skills("") {
		key := strings.ToLower(strings.TrimSpace(skill.ID))
		if key == "" || seen[key] || connector.IsReservedSkill(skill.ID) {
			continue
		}
		seen[key] = true
		definition, _ := s.deps.Registry.SkillDefinition(skill.ID)
		response.Skills = append(response.Skills, api.AgentSkillResponse{
			Presentation: skill.Presentation,
			ID:           skill.ID, Name: skill.Name, Description: skill.Description,
			Icon: agentSkillIconURL("", definition), Configured: configured[key],
		})
	}
	response.Packages, err = s.listAgentSkillPackages()
	return response, err
}

func (s *Server) updateAgentSkillPin(ctx context.Context, request api.UpdateAgentSkillPinRequest) (api.AgentSkillsResponse, error) {
	user, err := s.catalogOrderUser(ctx)
	if err != nil {
		return api.AgentSkillsResponse{}, err
	}
	key := strings.ToLower(strings.TrimSpace(request.ID))
	if key == "" || len(key) > 256 || catalog.ValidateEditableSkillID(key) != nil || request.Pinned == nil {
		return api.AgentSkillsResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "id and pinned are required")
	}
	if *request.Pinned && !s.knownPinnableSkill(key) {
		return api.AgentSkillsResponse{}, newAgentStatusError(http.StatusNotFound, "skill_not_found", "skill is not available")
	}
	state, err := s.skillOrder.SetPinned(user, key, *request.Pinned)
	if err != nil {
		return api.AgentSkillsResponse{}, err
	}
	response := skillPinsResponse(state)
	response.Packages, err = s.listAgentSkillPackages()
	return response, err
}

func (s *Server) knownPinnableSkill(id string) bool {
	if s.deps.Registry == nil || connector.IsReservedSkill(id) {
		return false
	}
	if registry, err := s.adminSkillRegistry(); err == nil {
		if _, found, err := registry.AdminSkill(id); err == nil && found {
			return true
		}
		// Package IDs are presentation pins, never executable SkillDefinitions.
		// Reuse the live package scan so missing or invalid packages cannot be pinned.
		if packages, err := registry.EditableSkillPackages(); err == nil {
			for _, pkg := range packages {
				if strings.EqualFold(pkg.ID, id) {
					return true
				}
			}
		}
	}
	for _, skill := range s.deps.Registry.Skills("") {
		if strings.EqualFold(strings.TrimSpace(skill.ID), id) {
			return true
		}
	}
	for _, agent := range s.deps.Registry.Agents("all") {
		definition, ok := s.deps.Registry.AgentDefinition(agent.Key)
		if !ok || definition.IsConnectorSkill(id) {
			continue
		}
		for _, configured := range definition.Skills {
			if strings.EqualFold(strings.TrimSpace(configured), id) {
				return true
			}
		}
	}
	return false
}

func skillPinsResponse(state catalogorder.OrderState) api.AgentSkillsResponse {
	pinned := state.Order
	if pinned == nil {
		pinned = []string{}
	}
	return api.AgentSkillsResponse{Skills: []api.AgentSkillResponse{}, Pinned: pinned}
}

func agentSkillsStatusError(status int, code string, message string) error {
	return newAgentStatusErrorWithData(status, code, message, map[string]any{
		"code":    code,
		"message": message,
	})
}
