package server

import (
	"net/http"
	"sort"

	"agent-platform-runner-go/internal/api"
)

func (s *Server) handleChannels(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, api.Success(s.listChannelSummaries()))
}

func (s *Server) listChannelSummaries() []api.ChannelSummary {
	if s.deps.Channels == nil {
		return nil
	}
	defs := s.deps.Channels.All()
	items := make([]api.ChannelSummary, 0, len(defs))
	for _, def := range defs {
		agents := s.channelAgentKeys(def.ID, def.AllAgents)
		items = append(items, api.ChannelSummary{
			ID:           def.ID,
			Name:         def.Name,
			Type:         string(def.Type),
			DefaultAgent: def.DefaultAgent,
			Agents:       agents,
			Connected:    s.channelConnected(def.ID),
		})
	}
	return items
}

func (s *Server) channelAgentKeys(channelID string, allAgents bool) []string {
	if allAgents || s.deps.Channels == nil {
		items := s.deps.Registry.Agents("")
		keys := make([]string, 0, len(items))
		for _, item := range items {
			keys = append(keys, item.Key)
		}
		sort.Strings(keys)
		return keys
	}
	return s.deps.Channels.AllowedAgentKeys(channelID)
}

func (s *Server) channelConnected(channelID string) bool {
	if s.deps.ChannelStatus == nil {
		return false
	}
	return s.deps.ChannelStatus.Connected(channelID)
}
