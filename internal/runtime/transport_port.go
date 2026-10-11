package runtime

import (
	"context"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts/queryinput"
	runtimetypes "agent-platform/internal/runtime/types"
)

func (s *Service) ValidateRunOwner(runID, agentKey string) *runtimetypes.RequestError {
	backend, err := s.current()
	if err != nil {
		return &runtimetypes.RequestError{Status: 500, Message: err.Error()}
	}
	return backend.ValidateRunOwner(runID, agentKey)
}
func (s *Service) PendingAwaitingInfo(chatID string, pending *chat.PendingAwaiting) (*queryinput.ChatErrorInfo, error) {
	backend, err := s.current()
	if err != nil {
		return nil, err
	}
	return backend.PendingAwaitingInfo(chatID, pending)
}
func (s *Service) RegisterPreparedQuery(ctx context.Context, prepared runtimetypes.PreparedQuery) (runtimetypes.RegisteredRun, *runtimetypes.RequestError) {
	backend, err := s.current()
	if err != nil {
		return runtimetypes.RegisteredRun{}, &runtimetypes.RequestError{Status: 500, Message: err.Error()}
	}
	return backend.RegisterPreparedQuery(ctx, prepared)
}
func (s *Service) FinishRegisteredQuery(prepared runtimetypes.PreparedQuery, registered runtimetypes.RegisteredRun) {
	backend, err := s.current()
	if err != nil {
		return
	}
	backend.FinishRegisteredQuery(prepared, registered)
}
