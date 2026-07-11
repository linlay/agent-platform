package server

import (
	"errors"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
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

func toAPIActiveRunInfo(activeRun contracts.RunStatusInfo) *api.ActiveRunInfo {
	return &api.ActiveRunInfo{
		RunID:     activeRun.RunID,
		OwnerType: string(activeRun.OwnerType),
		AgentKey:  activeRun.AgentKey,
		TeamID:    activeRun.TeamID,
		State:     string(activeRun.State),
		LastSeq:   activeRun.LastSeq,
		OldestSeq: activeRun.OldestSeq,
		StartedAt: activeRun.StartedAt,
	}
}

func (s *Server) listAgentSummaries(includeChats int, scope string) ([]api.AgentSummary, error) {
	items := s.deps.Registry.Agents(scope)
	if s.deps.Chats == nil {
		return items, nil
	}
	stats, err := s.deps.Chats.AgentChatStats()
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].Stats = toAPIAgentStats(stats[items[i].Key])
		if includeChats > 0 {
			chats, err := s.deps.Chats.RecentChatsByAgent(items[i].Key, includeChats)
			if err != nil {
				return nil, err
			}
			summaries, err := s.mapAgentChatSummaries(chats)
			if err != nil {
				return nil, err
			}
			items[i].Chats = summaries
		}
	}
	return items, nil
}

func (s *Server) mapAgentChatSummaries(items []chat.Summary) ([]api.ChatSummaryResponse, error) {
	response := mapChatSummariesWithoutUsage(items)
	if s.deps.Runs == nil {
		return response, nil
	}
	for i := range response {
		activeRun, ok, err := s.deps.Runs.ActiveRunForChat(response[i].ChatID)
		if err != nil {
			var conflictErr *contracts.ActiveRunConflictError
			if errors.As(err, &conflictErr) {
				response[i].Error = activeRunConflictInfo(conflictErr)
				continue
			}
			return nil, err
		}
		if ok {
			response[i].ActiveRun = toAPIActiveRunInfo(activeRun)
		}
	}
	return response, nil
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
