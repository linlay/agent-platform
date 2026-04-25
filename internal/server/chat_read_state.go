package server

import (
	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/chat"
)

func toAPIReadState(state chat.ChatReadState) api.ChatReadState {
	return api.ChatReadState{
		IsRead:    state.IsRead,
		ReadAt:    state.ReadAt,
		ReadRunID: state.ReadRunID,
	}
}

func toAPIAgentStats(state chat.AgentChatStats) api.AgentChatStats {
	return api.AgentChatStats{
		TotalCount:  state.TotalCount,
		UnreadCount: state.UnreadCount,
	}
}

func (s *Server) listAgentSummaries(tag string, channelID string) ([]api.AgentSummary, error) {
	items := s.deps.Registry.Agents(tag)
	items = s.filterAgentSummariesByChannel(items, channelID)
	if s.deps.Chats == nil {
		return items, nil
	}
	stats, err := s.deps.Chats.AgentChatStats()
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].Stats = toAPIAgentStats(stats[items[i].Key])
	}
	return items, nil
}

func (s *Server) filterAgentSummariesByChannel(items []api.AgentSummary, channelID string) []api.AgentSummary {
	if s.deps.Channels == nil {
		return items
	}
	if _, ok := s.deps.Channels.Lookup(channelID); !ok {
		return items
	}
	filtered := make([]api.AgentSummary, 0, len(items))
	for _, item := range items {
		if s.deps.Channels.IsAgentAllowed(channelID, item.Key) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func (s *Server) agentUnreadCount(agentKey string) (int, error) {
	if s.deps.Chats == nil {
		return 0, nil
	}
	stats, err := s.deps.Chats.AgentChatStats()
	if err != nil {
		return 0, err
	}
	return stats[agentKey].UnreadCount, nil
}

func chatPushReadAt(state chat.ChatReadState) int64 {
	if state.ReadAt == nil {
		return 0
	}
	return *state.ReadAt
}

func (s *Server) buildMarkReadResponse(sum chat.Summary, agentUnreadCount int) api.MarkChatReadResponse {
	return api.MarkChatReadResponse{
		ChatID:           sum.ChatID,
		AgentKey:         sum.AgentKey,
		LastRunID:        sum.LastRunID,
		Read:             toAPIReadState(sum.Read),
		AgentUnreadCount: agentUnreadCount,
	}
}

func (s *Server) broadcastChatReadState(eventType string, sum chat.Summary, agentUnreadCount int) {
	s.broadcast(eventType, map[string]any{
		"chatId":           sum.ChatID,
		"agentKey":         sum.AgentKey,
		"lastRunId":        sum.LastRunID,
		"readAt":           chatPushReadAt(sum.Read),
		"readRunId":        sum.Read.ReadRunID,
		"agentUnreadCount": agentUnreadCount,
	})
}
