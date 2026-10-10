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
		RunID:    activeRun.RunID,
		AgentKey: activeRun.AgentKey,

		State:       string(activeRun.State),
		LastSeq:     activeRun.LastSeq,
		OldestSeq:   activeRun.OldestSeq,
		StartedAt:   activeRun.StartedAt,
		EditingMode: activeRun.EditingMode,
	}
}

func (s *Server) listAgentSummariesWithPinned(includeChats int, scope string, modes []string, pinned *bool, hasWorkspace *bool) ([]api.AgentSummary, error) {
	items := s.filteredAgentSummaries(scope, modes, hasWorkspace)
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
			chats, err := s.conversationService().RecentSummaries(items[i].Key, includeChats, pinned)
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

func (s *Server) filteredAgentSummaries(scope string, modes []string, hasWorkspace *bool) []api.AgentSummary {
	items := s.deps.Registry.Agents(scope)
	if hasWorkspace != nil {
		filtered := make([]api.AgentSummary, 0, len(items))
		for _, item := range items {
			if agentHasWorkspace(item) == *hasWorkspace {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	if modes = chat.NormalizeAgentModes(modes); len(modes) > 0 {
		allowed := make(map[string]struct{}, len(modes))
		for _, mode := range modes {
			allowed[mode] = struct{}{}
		}
		filtered := make([]api.AgentSummary, 0, len(items))
		for _, item := range items {
			if _, ok := allowed[item.Mode]; ok {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	return items
}

func (s *Server) mapAgentChatSummaries(items []chat.Summary) ([]api.ChatSummaryResponse, error) {
	return s.mapChatSummariesWithActiveRuns(items)
}

// mapChatSummariesWithActiveRuns enriches persisted chat summaries with the
// in-memory run state shared by Chat navigation lists. Usage and continuation
// eligibility are omitted; active-run and conflict semantics remain identical.
func (s *Server) mapChatSummariesWithActiveRuns(items []chat.Summary) ([]api.ChatSummaryResponse, error) {
	response := mapChatSummariesWithUsage(items, false)
	if s.deps.Runs == nil {
		return response, nil
	}
	for i := range response {
		activeRun, ok, err := s.conversationService().ActiveRun(response[i].ChatID)
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

func (s *Server) buildMarkReadResponse(sum chat.Summary, agentUnreadCount int) api.MarkChatReadResponse {
	return api.MarkChatReadResponse{
		ChatID:           sum.ChatID,
		AgentKey:         sum.AgentKey,
		LastRunID:        sum.LastRunID,
		Read:             toAPIReadState(sum.Read),
		AgentUnreadCount: agentUnreadCount,
	}
}

func buildChatReadStatePush(eventType string, sum chat.Summary, agentUnreadCount int) (map[string]any, bool) {
	if eventType == "chat.unread" && !chat.RunIDAfter(sum.LastRunID, sum.Read.ReadRunID) {
		// MarkRead may win the race after run completion has persisted its
		// summary but before the completion callback broadcasts. Never emit an
		// unread projection that the current persisted summary already disproves.
		return nil, false
	}
	payload := map[string]any{
		"chatId":           sum.ChatID,
		"agentKey":         sum.AgentKey,
		"lastRunId":        sum.LastRunID,
		"readRunId":        sum.Read.ReadRunID,
		"agentUnreadCount": agentUnreadCount,
	}
	switch eventType {
	case "chat.unread":
		// A newly completed run made this chat unread. The chat summary uses the
		// same persisted instant for its update and this new unread state.
		payload["createdAt"] = sum.UpdatedAt
	case "chat.read":
		// A zero sentinel would be a fabricated 1970 time under the public
		// contract, so keep it absent until a real read was recorded.
		if sum.Read.ReadAt != nil {
			payload["readAt"] = *sum.Read.ReadAt
		}
	}
	return payload, true
}

func (s *Server) broadcastChatReadState(eventType string, sum chat.Summary, agentUnreadCount int) {
	if eventType == "chat.unread" {
		// Completion callbacks may carry a summary that was current when the run
		// committed but became stale because /api/read won immediately after it.
		// Re-read the authority immediately before constructing the Push.
		current, err := s.deps.Chats.Summary(sum.ChatID)
		if err != nil || current == nil {
			return
		}
		sum = *current
		agentUnreadCount, err = s.agentUnreadCount(sum.AgentKey)
		if err != nil {
			return
		}
	}
	payload, ok := buildChatReadStatePush(eventType, sum, agentUnreadCount)
	if !ok {
		return
	}
	s.broadcast(eventType, payload)
}
