package server

import (
	"context"

	"agent-platform/internal/api"
	"agent-platform/internal/runtime/reference"
)

func (s *Server) prepareQueryReferences(ctx context.Context, chatID string, refs []api.Reference) ([]api.Reference, error) {
	return reference.New(s.deps.Chats).Prepare(ctx, chatID, refs)
}

var prepareSiteReference = reference.PrepareSiteReference
var buildChatReferenceContext = reference.BuildChatReferenceContext

func (s *Server) prepareChatReference(ctx context.Context, chatID string, ref api.Reference) (api.Reference, error) {
	return reference.New(s.deps.Chats).PrepareChatReference(ctx, chatID, ref)
}
