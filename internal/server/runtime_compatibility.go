package server

import (
	"context"
	"fmt"
	"strings"

	"agent-platform/internal/contracts"
	runtimetypes "agent-platform/internal/runtime/types"
)

// runtimeCompatibilityAdapter exists only for callers that construct Server
// directly without app.New. The production assembly always injects
// internal/runtime.Service; keeping this adapter local avoids making server
// responsible for composing Runtime packages.
type runtimeCompatibilityAdapter struct{ server *Server }

func (a *runtimeCompatibilityAdapter) StartQuery(ctx context.Context, command runtimetypes.QueryCommand) (runtimetypes.RunHandle, error) {
	return a.server.StartQueryRuntime(ctx, command)
}

func (a *runtimeCompatibilityAdapter) AttachRun(_ context.Context, ref runtimetypes.RunRef, afterSeq int64) (*runtimetypes.Subscription, error) {
	if a == nil || a.server == nil || a.server.deps.Runs == nil {
		return nil, fmt.Errorf("run state is not configured")
	}
	if strings.TrimSpace(ref.RunID) == "" || afterSeq < 0 {
		return nil, fmt.Errorf("runId is required and afterSeq must not be negative")
	}
	observer, err := a.server.deps.Runs.AttachObserver(ref.RunID, afterSeq)
	if err != nil {
		return nil, err
	}
	return runtimetypes.NewSubscription(observer.ID, observer.Events, func() {
		a.server.deps.Runs.DetachObserver(ref.RunID, observer.ID)
		observer.MarkDone()
	}), nil
}

func (a *runtimeCompatibilityAdapter) Submit(ctx context.Context, command runtimetypes.SubmitCommand) (runtimetypes.SubmitResult, error) {
	return a.server.SubmitRuntime(ctx, command)
}

func (a *runtimeCompatibilityAdapter) Steer(ctx context.Context, command runtimetypes.SteerCommand) (runtimetypes.SteerResult, error) {
	return a.server.SteerRuntime(ctx, command)
}

func (a *runtimeCompatibilityAdapter) Interrupt(ctx context.Context, command runtimetypes.InterruptCommand) (runtimetypes.InterruptResult, error) {
	return a.server.InterruptRuntime(ctx, command)
}

func (a *runtimeCompatibilityAdapter) SetAccessLevel(ctx context.Context, command runtimetypes.AccessLevelCommand) (runtimetypes.AccessLevelResult, error) {
	return a.server.SetAccessLevelRuntime(ctx, command)
}

func (a *runtimeCompatibilityAdapter) StartRun(ctx context.Context, request contracts.RunStartRequest) (contracts.RunSnapshot, error) {
	return a.server.StartRun(ctx, request)
}

func (a *runtimeCompatibilityAdapter) GetRunStatus(runID string) (contracts.RunSnapshot, error) {
	return a.server.GetRunStatus(runID)
}
