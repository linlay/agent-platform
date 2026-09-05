// Package runtime is the transport-neutral application facade for queries and
// run control. HTTP, WebSocket, automation, and run tools all enter through
// this boundary.
package runtime

import (
	"context"
	"errors"
	"sync"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	runtimetypes "agent-platform/internal/runtime/types"
)

var ErrBackendUnavailable = errors.New("runtime backend is not configured")

// Backend is the migration seam implemented by the query runtime. It is
// deliberately declared here so runtime never imports the server transport.
type Backend interface {
	StartQuery(context.Context, runtimetypes.QueryCommand) (runtimetypes.RunHandle, error)
	ExecuteQuery(context.Context, runtimetypes.QueryCommand, runtimetypes.QueryHooks) (runtimetypes.QueryResult, error)
	StartRun(context.Context, contracts.RunStartRequest) (contracts.RunSnapshot, error)
	RunStatus(string) (contracts.RunSnapshot, error)
	AttachRun(context.Context, runtimetypes.RunRef, int64) (*runtimetypes.Subscription, error)
	Submit(context.Context, runtimetypes.SubmitCommand) (runtimetypes.SubmitResult, error)
	Steer(context.Context, runtimetypes.SteerCommand) (runtimetypes.SteerResult, error)
	Interrupt(context.Context, runtimetypes.InterruptCommand) (runtimetypes.InterruptResult, error)
	SetAccessLevel(context.Context, runtimetypes.AccessLevelCommand) (runtimetypes.AccessLevelResult, error)
}

func (s *Service) StartQuery(ctx context.Context, command runtimetypes.QueryCommand) (runtimetypes.RunHandle, error) {
	backend, err := s.current()
	if err != nil {
		return runtimetypes.RunHandle{}, err
	}
	return backend.StartQuery(ctx, command)
}

type Service struct {
	mu      sync.RWMutex
	backend Backend
}

func NewService() *Service {
	return &Service{}
}

// Bind installs the runtime implementation during app assembly. Calls are
// safe after Bind returns; rebinding is supported for tests and hot assembly.
func (s *Service) Bind(backend Backend) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.backend = backend
	s.mu.Unlock()
}

func (s *Service) current() (Backend, error) {
	if s == nil {
		return nil, ErrBackendUnavailable
	}
	s.mu.RLock()
	backend := s.backend
	s.mu.RUnlock()
	if backend == nil {
		return nil, ErrBackendUnavailable
	}
	return backend, nil
}

func (s *Service) ExecuteQuery(ctx context.Context, cmd runtimetypes.QueryCommand, sink runtimetypes.EventSink) (runtimetypes.QueryResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if sink == nil {
		return s.ExecuteQueryWithHooks(ctx, cmd, runtimetypes.QueryHooks{})
	}
	backend, err := s.current()
	if err != nil {
		return runtimetypes.QueryResult{}, err
	}
	var (
		forwardWG  sync.WaitGroup
		forwardErr error
		errMu      sync.Mutex
	)
	hooks := runtimetypes.QueryHooks{OnRunStarted: func(start chat.RunStart) {
		if runStartSink, ok := sink.(runtimetypes.RunStartSink); ok {
			runStartSink.OnRunStarted(start)
		}
		subscription, attachErr := backend.AttachRun(ctx, runtimetypes.RunRef{RunID: start.RunID, ChatID: start.ChatID}, 0)
		if attachErr != nil {
			errMu.Lock()
			forwardErr = attachErr
			errMu.Unlock()
			return
		}
		forwardWG.Add(1)
		go func() {
			defer forwardWG.Done()
			defer subscription.Close()
			for event := range subscription.Events {
				if emitErr := sink.Emit(ctx, event); emitErr != nil {
					errMu.Lock()
					if forwardErr == nil {
						forwardErr = emitErr
					}
					errMu.Unlock()
					return
				}
			}
		}()
	}}
	result, executeErr := backend.ExecuteQuery(ctx, cmd, hooks)
	forwardWG.Wait()
	errMu.Lock()
	sinkErr := forwardErr
	errMu.Unlock()
	if executeErr != nil {
		return result, executeErr
	}
	if sinkErr != nil {
		return result, sinkErr
	}
	return result, nil
}

func (s *Service) ExecuteQueryWithHooks(ctx context.Context, cmd runtimetypes.QueryCommand, hooks runtimetypes.QueryHooks) (runtimetypes.QueryResult, error) {
	backend, err := s.current()
	if err != nil {
		return runtimetypes.QueryResult{}, err
	}
	return backend.ExecuteQuery(ctx, cmd, hooks)
}

func (s *Service) StartRun(ctx context.Context, request contracts.RunStartRequest) (contracts.RunSnapshot, error) {
	backend, err := s.current()
	if err != nil {
		return contracts.RunSnapshot{}, err
	}
	return backend.StartRun(ctx, request)
}

func (s *Service) GetRunStatus(runID string) (contracts.RunSnapshot, error) {
	return s.RunStatus(context.Background(), runID)
}

func (s *Service) RunStatus(_ context.Context, runID string) (contracts.RunSnapshot, error) {
	backend, err := s.current()
	if err != nil {
		return contracts.RunSnapshot{}, err
	}
	return backend.RunStatus(runID)
}

func (s *Service) AttachRun(ctx context.Context, ref runtimetypes.RunRef, afterSeq int64) (*runtimetypes.Subscription, error) {
	backend, err := s.current()
	if err != nil {
		return nil, err
	}
	return backend.AttachRun(ctx, ref, afterSeq)
}

func (s *Service) Submit(ctx context.Context, command runtimetypes.SubmitCommand) (runtimetypes.SubmitResult, error) {
	backend, err := s.current()
	if err != nil {
		return runtimetypes.SubmitResult{}, err
	}
	return backend.Submit(ctx, command)
}

func (s *Service) Steer(ctx context.Context, command runtimetypes.SteerCommand) (runtimetypes.SteerResult, error) {
	backend, err := s.current()
	if err != nil {
		return runtimetypes.SteerResult{}, err
	}
	return backend.Steer(ctx, command)
}

func (s *Service) Interrupt(ctx context.Context, command runtimetypes.InterruptCommand) (runtimetypes.InterruptResult, error) {
	backend, err := s.current()
	if err != nil {
		return runtimetypes.InterruptResult{}, err
	}
	return backend.Interrupt(ctx, command)
}

func (s *Service) SetAccessLevel(ctx context.Context, command runtimetypes.AccessLevelCommand) (runtimetypes.AccessLevelResult, error) {
	backend, err := s.current()
	if err != nil {
		return runtimetypes.AccessLevelResult{}, err
	}
	return backend.SetAccessLevel(ctx, command)
}
