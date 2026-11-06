package server

import (
	"agent-platform/internal/api"
	"net/http"
)

func (s *Server) handleRuntimeCapabilities(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	keys := []string{}
	if policy, ok := s.deps.Runtime.(interface{ PrimaryChatEnabledForAgent(string) bool }); ok {
		for _, agent := range s.deps.Registry.Agents("") {
			if policy.PrimaryChatEnabledForAgent(agent.Key) {
				keys = append(keys, agent.Key)
			}
		}
	}
	writeJSON(w, http.StatusOK, api.Success(map[string]any{"primaryChat": map[string]any{"enabled": s.deps.Config.PrimaryChat.Enabled, "agentKeys": keys}}))
}
