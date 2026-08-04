package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/ws"
)

func (s *Server) handleAgentSkills(w http.ResponseWriter, r *http.Request) {
	response, err := s.listSkillsForAgent(r.URL.Query().Get("agentKey"))
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) wsAgentSkills(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[struct {
		AgentKey string `json:"agentKey"`
	}](req)
	if err != nil {
		s.sendAgentWSError(conn, req, agentSkillsStatusError(http.StatusBadRequest, "invalid_request", "invalid payload"))
		return
	}
	response, listErr := s.listSkillsForAgent(payload.AgentKey)
	s.sendAgentWSResponse(conn, req, response, listErr)
}

func (s *Server) listSkillsForAgent(agentKey string) (api.AgentSkillsResponse, error) {
	agentKey = strings.TrimSpace(agentKey)
	if agentKey == "" {
		return api.AgentSkillsResponse{}, agentSkillsStatusError(http.StatusBadRequest, "agent_key_required", "agentKey is required")
	}
	if s == nil || s.deps.Registry == nil {
		return api.AgentSkillsResponse{}, agentSkillsStatusError(http.StatusServiceUnavailable, "skill_catalog_unavailable", "skill catalog is not configured")
	}
	definition, ok := s.deps.Registry.AgentDefinition(agentKey)
	if !ok {
		return api.AgentSkillsResponse{}, agentSkillsStatusError(http.StatusNotFound, "agent_not_found", "agent not found")
	}

	marketSkills := s.deps.Registry.Skills("")
	response := api.AgentSkillsResponse{
		AgentKey: definition.Key,
		Skills:   make([]api.AgentSkillResponse, 0, len(marketSkills)+len(definition.Skills)),
	}
	seen := make(map[string]struct{}, len(marketSkills)+len(definition.Skills))
	for _, configuredKey := range definition.Skills {
		configuredKey = strings.TrimSpace(configuredKey)
		normalized := strings.ToLower(configuredKey)
		if normalized == "" {
			continue
		}
		if _, duplicate := seen[normalized]; duplicate {
			continue
		}
		runtimeSkill, found, err := catalog.ResolveRuntimeSkillDefinition(definition.RuntimeDir, configuredKey)
		if err != nil {
			return api.AgentSkillsResponse{}, agentSkillsStatusError(
				http.StatusServiceUnavailable,
				"skill_catalog_unavailable",
				fmt.Sprintf("resolve configured skill %q: %v", configuredKey, err),
			)
		}
		if !found {
			return api.AgentSkillsResponse{}, agentSkillsStatusError(
				http.StatusServiceUnavailable,
				"skill_catalog_unavailable",
				fmt.Sprintf("configured skill %q is unavailable in agent runtime", configuredKey),
			)
		}
		seen[normalized] = struct{}{}
		response.Skills = append(response.Skills, api.AgentSkillResponse{
			Key:           runtimeSkill.Key,
			Name:          runtimeSkill.Name,
			Description:   runtimeSkill.Description,
			AgentHasSkill: true,
		})
	}
	for _, marketSkill := range marketSkills {
		normalized := strings.ToLower(strings.TrimSpace(marketSkill.Key))
		if normalized == "" {
			continue
		}
		if _, duplicate := seen[normalized]; duplicate {
			continue
		}
		seen[normalized] = struct{}{}
		response.Skills = append(response.Skills, api.AgentSkillResponse{
			Key:         marketSkill.Key,
			Name:        marketSkill.Name,
			Description: marketSkill.Description,
		})
	}
	return response, nil
}

func agentSkillsStatusError(status int, code string, message string) error {
	return newAgentStatusErrorWithData(status, code, message, map[string]any{
		"code":    code,
		"message": message,
	})
}
