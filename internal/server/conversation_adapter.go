package server

import "agent-platform/internal/conversation"

// conversationService refreshes the service view from the Server dependency
// set. Production assembly injects a fully configured service; the refresh is
// also important for embedders and package tests that replace a store or run
// manager after constructing the Server.
func (s *Server) conversationService() *conversation.Service {
	if s == nil {
		return nil
	}
	var service conversation.Service
	if s.deps.Conversation != nil {
		service = *s.deps.Conversation
	}
	if s.deps.Chats != nil {
		service.Chats = s.deps.Chats
	}
	if s.deps.Archives != nil {
		service.Archives = s.deps.Archives
	}
	if s.deps.Archiver != nil {
		service.Archiver = s.deps.Archiver
	}
	if s.deps.Runs != nil {
		service.Runs = s.deps.Runs
	}
	return &service
}
