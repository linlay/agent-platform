package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/chat"
)

func (s *Server) validateRunOwner(runID, agentKey string) *statusError {
	return s.deps.Runtime.ValidateRunOwner(runID, agentKey)
}
func (s *Server) validPendingAwaitingInfo(chatID string, pending *chat.PendingAwaiting) (*api.ChatErrorInfo, error) {
	return s.deps.Runtime.PendingAwaitingInfo(chatID, pending)
}
