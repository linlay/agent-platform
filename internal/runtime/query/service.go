// Package query coordinates query admission, session preparation,
// continuation, and run-control entry points without depending on transport.
package query

import (
	"context"
	"errors"
	"strings"

	"agent-platform/internal/apperrors"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/runtime/runstate"
	runtimetypes "agent-platform/internal/runtime/types"
)

var ErrNotConfigured = errors.New("query runtime is not configured")

type ExecuteFunc func(context.Context, runtimetypes.QueryCommand, runtimetypes.QueryHooks) (runtimetypes.QueryResult, error)
type StartQueryFunc func(context.Context, runtimetypes.QueryCommand) (runtimetypes.RunHandle, error)
type StartFunc func(context.Context, contracts.RunStartRequest) (contracts.RunSnapshot, error)
type SubmitFunc func(context.Context, runtimetypes.SubmitCommand) (runtimetypes.SubmitResult, error)
type SteerFunc func(context.Context, runtimetypes.SteerCommand) (runtimetypes.SteerResult, error)
type InterruptFunc func(context.Context, runtimetypes.InterruptCommand) (runtimetypes.InterruptResult, error)
type AccessLevelFunc func(context.Context, runtimetypes.AccessLevelCommand) (runtimetypes.AccessLevelResult, error)

type Dependencies struct {
	Runs       contracts.RunManager
	Chats      chat.Store
	Execute    ExecuteFunc
	StartQuery StartQueryFunc
	Start      StartFunc
	Submit     SubmitFunc
	Steer      SteerFunc
	Interrupt  InterruptFunc
	Access     AccessLevelFunc
}

type Service struct {
	deps Dependencies
}

func NewService(deps Dependencies) *Service {
	return &Service{deps: deps}
}

func (s *Service) StartQuery(ctx context.Context, command runtimetypes.QueryCommand) (runtimetypes.RunHandle, error) {
	if s == nil || s.deps.StartQuery == nil {
		return runtimetypes.RunHandle{}, ErrNotConfigured
	}
	return s.deps.StartQuery(ctx, command)
}

func (s *Service) ExecuteQuery(ctx context.Context, cmd runtimetypes.QueryCommand, hooks runtimetypes.QueryHooks) (runtimetypes.QueryResult, error) {
	if s == nil || s.deps.Execute == nil {
		return runtimetypes.QueryResult{}, ErrNotConfigured
	}
	return s.deps.Execute(ctx, cmd, hooks)
}

func (s *Service) StartRun(ctx context.Context, request contracts.RunStartRequest) (contracts.RunSnapshot, error) {
	if s == nil || s.deps.Start == nil {
		return contracts.RunSnapshot{}, ErrNotConfigured
	}
	return s.deps.Start(ctx, request)
}

func (s *Service) RunStatus(runID string) (contracts.RunSnapshot, error) {
	if s == nil || s.deps.Runs == nil {
		return contracts.RunSnapshot{}, ErrNotConfigured
	}
	return runstate.Snapshot(s.deps.Runs, s.deps.Chats, runID)
}

func (s *Service) AttachRun(_ context.Context, ref runtimetypes.RunRef, afterSeq int64) (*runtimetypes.Subscription, error) {
	if s == nil || s.deps.Runs == nil {
		return nil, ErrNotConfigured
	}
	if strings.TrimSpace(ref.RunID) == "" || afterSeq < 0 {
		return nil, apperrors.New(apperrors.CodeInvalidRequest, "runId is required and afterSeq must not be negative")
	}
	observer, err := s.deps.Runs.AttachObserver(ref.RunID, afterSeq)
	if err != nil {
		return nil, err
	}
	return runtimetypes.NewSubscription(observer.ID, observer.Events, func() {
		s.deps.Runs.DetachObserver(ref.RunID, observer.ID)
		// FreezeAndWait removes live observers from the bus before closing their
		// channels, so DetachObserver alone can no longer find the observer to
		// acknowledge delivery completion. Keep the acknowledgement on the
		// subscription boundary just like the legacy transport adapters did.
		observer.MarkDone()
	}), nil
}

func (s *Service) Submit(ctx context.Context, command runtimetypes.SubmitCommand) (runtimetypes.SubmitResult, error) {
	if strings.TrimSpace(command.RunID) == "" || strings.TrimSpace(command.AwaitingID) == "" {
		return runtimetypes.SubmitResult{}, apperrors.New(apperrors.CodeInvalidRequest, "runId and awaitingId are required")
	}
	if s == nil || s.deps.Submit == nil {
		return runtimetypes.SubmitResult{}, ErrNotConfigured
	}
	return s.deps.Submit(ctx, command)
}

func (s *Service) Steer(ctx context.Context, command runtimetypes.SteerCommand) (runtimetypes.SteerResult, error) {
	if strings.TrimSpace(command.RunID) == "" || strings.TrimSpace(command.Message) == "" {
		return runtimetypes.SteerResult{}, apperrors.New(apperrors.CodeInvalidRequest, "runId and message are required")
	}
	if s == nil || s.deps.Steer == nil {
		return runtimetypes.SteerResult{}, ErrNotConfigured
	}
	return s.deps.Steer(ctx, command)
}

func (s *Service) Interrupt(ctx context.Context, command runtimetypes.InterruptCommand) (runtimetypes.InterruptResult, error) {
	if strings.TrimSpace(command.RunID) == "" {
		return runtimetypes.InterruptResult{}, apperrors.New(apperrors.CodeInvalidRequest, "runId is required")
	}
	if s == nil || s.deps.Interrupt == nil {
		return runtimetypes.InterruptResult{}, ErrNotConfigured
	}
	return s.deps.Interrupt(ctx, command)
}

func (s *Service) SetAccessLevel(ctx context.Context, command runtimetypes.AccessLevelCommand) (runtimetypes.AccessLevelResult, error) {
	if strings.TrimSpace(command.RunID) == "" {
		return runtimetypes.AccessLevelResult{}, apperrors.New(apperrors.CodeInvalidRequest, "runId is required")
	}
	if s == nil || s.deps.Access == nil {
		return runtimetypes.AccessLevelResult{}, ErrNotConfigured
	}
	return s.deps.Access(ctx, command)
}
