package server

import "agent-platform/internal/conversation"

func (s *Server) conversationService() *conversation.Service {
	if s == nil {
		return nil
	}
	service := s.deps.Conversation
	if service == nil {
		service = conversation.NewService(s.deps.Chats, s.deps.Archives, s.deps.Archiver, s.deps.Runs)
	}
	return service.WithDependencies(s.deps.Chats, s.deps.Archives, s.deps.Archiver, s.deps.Runs, s.deps.Notifications)
}
