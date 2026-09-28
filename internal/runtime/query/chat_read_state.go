package query

import (
	"agent-platform/internal/chat"
)

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

func (s *Service) broadcastChatReadState(eventType string, sum chat.Summary, agentUnreadCount int) {
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

func (s *Service) agentUnreadCount(agentKey string) (int, error) {
	if s.deps.Chats == nil {
		return 0, nil
	}
	stats, err := s.deps.Chats.AgentChatStats()
	if err != nil {
		return 0, err
	}
	return stats[agentKey].UnreadCount, nil
}
