package server

import (
	"errors"
	"sort"

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
		AgentKey:  activeRun.AgentKey,
		TeamID:    activeRun.TeamID,
		State:     string(activeRun.State),
		LastSeq:   activeRun.LastSeq,
		OldestSeq: activeRun.OldestSeq,
		StartedAt: activeRun.StartedAt,
	}
}

func (s *Server) listAgentSummaries(includeChats int, scope string) ([]api.AgentSummary, error) {
	return s.listAgentSummariesWithModes(includeChats, scope, nil)
}

func (s *Server) listAgentSummariesWithModes(includeChats int, scope string, modes []string) ([]api.AgentSummary, error) {
	items := s.filteredAgentSummaries(scope, modes)
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

func (s *Server) filteredAgentSummaries(scope string, modes []string) []api.AgentSummary {
	items := s.deps.Registry.Agents(scope)
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

type orderedAgentCatalogSummary struct {
	item      api.AgentCatalogSummary
	lastRunID string
	identity  string
}

func agentCatalogSummary(agent api.AgentSummary) api.AgentCatalogSummary {
	return api.AgentCatalogSummary{
		Kind:                   "agent",
		Key:                    agent.Key,
		Name:                   agent.Name,
		Icon:                   agent.Icon,
		Mode:                   agent.Mode,
		WorkspaceDir:           agent.WorkspaceDir,
		DefaultModelKey:        agent.DefaultModelKey,
		DefaultReasoningEffort: agent.DefaultReasoningEffort,
		ModelConfig:            agent.ModelConfig,
		ModelOptions:           agent.ModelOptions,
		Role:                   agent.Role,
		Stats:                  agent.Stats,
		Chats:                  agent.Chats,
	}
}

// listAgentCatalogSummariesWithModes builds the opt-in mixed Team/Agent
// navigation catalog. Scope and mode are intentionally only applied before
// this point, while enumerating ordinary agents.
func (s *Server) listAgentCatalogSummariesWithModes(includeChats int, scope string, modes []string) ([]api.AgentCatalogSummary, error) {
	agents := s.filteredAgentSummaries(scope, modes)
	teams := s.deps.Registry.Teams()
	agentStats := map[string]chat.AgentChatStats{}
	teamStats := map[string]chat.AgentChatStats{}
	if s.deps.Chats != nil {
		var err error
		agentStats, err = s.deps.Chats.AgentChatStats()
		if err != nil {
			return nil, err
		}
		teamStats, err = s.deps.Chats.TeamChatStats()
		if err != nil {
			return nil, err
		}
	}

	items := make([]orderedAgentCatalogSummary, 0, len(agents)+len(teams))
	for i := range agents {
		agent := agents[i]
		stats := agentStats[agent.Key]
		agent.Stats = toAPIAgentStats(stats)
		if includeChats > 0 && s.deps.Chats != nil {
			chats, err := s.deps.Chats.RecentChatsByAgent(agent.Key, includeChats)
			if err != nil {
				return nil, err
			}
			summaries, err := s.mapAgentChatSummaries(chats)
			if err != nil {
				return nil, err
			}
			agent.Chats = summaries
		}
		items = append(items, orderedAgentCatalogSummary{
			item:      agentCatalogSummary(agent),
			lastRunID: stats.LastRunID,
			identity:  agent.Key,
		})
	}
	for i := range teams {
		team := teams[i]
		stats := teamStats[team.TeamID]
		summary := api.AgentCatalogSummary{
			Kind:        "team",
			Name:        team.Name,
			Icon:        team.Icon,
			Stats:       toAPIAgentStats(stats),
			TeamID:      team.TeamID,
			Description: team.Description,
			RuntimeMode: team.RuntimeMode,
			AgentKeys:   append([]string(nil), team.AgentKeys...),
			Meta:        team.Meta,
		}
		if includeChats > 0 && s.deps.Chats != nil {
			chats, err := s.deps.Chats.RecentChatsByTeam(team.TeamID, includeChats)
			if err != nil {
				return nil, err
			}
			summaries, err := s.mapAgentChatSummaries(chats)
			if err != nil {
				return nil, err
			}
			summary.Chats = summaries
		}
		items = append(items, orderedAgentCatalogSummary{
			item:      summary,
			lastRunID: stats.LastRunID,
			identity:  team.TeamID,
		})
	}

	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i], items[j]
		switch {
		case left.lastRunID != right.lastRunID:
			if left.lastRunID == "" {
				return false
			}
			if right.lastRunID == "" {
				return true
			}
			return chat.RunIDAfter(left.lastRunID, right.lastRunID)
		case left.item.Name != right.item.Name:
			return left.item.Name < right.item.Name
		case left.item.Kind != right.item.Kind:
			return left.item.Kind < right.item.Kind
		default:
			return left.identity < right.identity
		}
	})

	result := make([]api.AgentCatalogSummary, 0, len(items))
	for _, item := range items {
		result = append(result, item.item)
	}
	return result, nil
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
	s.broadcast(eventType, payload)
}
