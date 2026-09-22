package server

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"agent-platform/internal/catalog"
	"agent-platform/internal/connector"
)

func agentSkillIconURL(agentKey string, skill catalog.SkillDefinition) string {
	if skill.IconPath == "" {
		return ""
	}
	params := url.Values{"key": {skill.Key}}
	if agentKey != "" {
		params.Set("agentKey", agentKey)
	}
	return "/api/skills/icon?" + params.Encode()
}

func (s *Server) handleAgentSkillIcon(w http.ResponseWriter, r *http.Request) {
	key, agentKey := strings.TrimSpace(r.URL.Query().Get("key")), strings.TrimSpace(r.URL.Query().Get("agentKey"))
	if catalog.ValidateEditableSkillKey(key) != nil {
		s.writeAgentHTTPResponse(w, nil, newAgentStatusError(http.StatusBadRequest, "invalid_request", "a valid skill key is required"))
		return
	}
	def, found := s.deps.Registry.AgentDefinition(agentKey)
	if agentKey != "" && !found {
		s.writeAgentHTTPResponse(w, nil, newAgentStatusError(http.StatusNotFound, "agent_not_found", "agent not found"))
		return
	}
	var skill catalog.SkillDefinition
	if !connector.IsReservedSkill(key) && !def.IsConnectorSkill(key) {
		skill, found = s.deps.Registry.SkillDefinition(key)
		for _, configured := range def.Skills {
			if strings.EqualFold(configured, key) {
				var err error
				skill, found, err = catalog.ResolveRuntimeSkillDefinition(def.RuntimeDir, configured)
				if err != nil {
					found = false
				}
				break
			}
		}
	} else {
		found = false
	}
	if !found {
		s.writeAgentHTTPResponse(w, nil, newAgentStatusError(http.StatusNotFound, "skill_not_found", "skill not found"))
		return
	}
	data, err := catalog.ReadSkillIcon(skill)
	if err != nil {
		s.writeAgentHTTPResponse(w, nil, newAgentStatusError(http.StatusNotFound, "skill_icon_unavailable", "skill icon is unavailable"))
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	w.Header().Set("ETag", fmt.Sprintf(`"%x"`, sha256.Sum256(data)))
	http.ServeContent(w, r, "icon.png", time.Time{}, bytes.NewReader(data))
}
