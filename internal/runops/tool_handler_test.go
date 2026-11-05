package runops

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"agent-platform/internal/contracts"
	runtimetypes "agent-platform/internal/runtime/types"
)

type fakeRunToolService struct {
	mu          sync.Mutex
	startErr    error
	starts      int
	requests    []contracts.RunStartRequest
	snapshots   map[string]contracts.RunSnapshot
	interrupts  []runtimetypes.InterruptCommand
	steerCalls  []runtimetypes.SteerCommand
	steerResult runtimetypes.SteerResult
	steerErr    error
}

func newFakeRunToolService() *fakeRunToolService {
	return &fakeRunToolService{snapshots: map[string]contracts.RunSnapshot{}}
}

func (f *fakeRunToolService) StartRun(_ context.Context, req contracts.RunStartRequest) (contracts.RunSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	f.requests = append(f.requests, req)
	runID := fmt.Sprintf("target-%d", f.starts)
	if f.startErr != nil {
		err := f.startErr
		var typed *contracts.RunToolError
		if errors.As(err, &typed) && typed.ExecutionState == "unknown" {
			f.snapshots[typed.RunID] = contracts.RunSnapshot{RunID: typed.RunID, ChatID: typed.ChatID, Status: "running", Origin: &req.Origin}
		}
		return contracts.RunSnapshot{}, err
	}
	snapshot := contracts.RunSnapshot{
		RunID:     runID,
		ChatID:    "chat-" + runID,
		AgentKey:  req.AgentKey,
		Status:    "running",
		StartedAt: 1700000000000,
		Origin:    &req.Origin,
	}
	f.snapshots[runID] = snapshot
	return snapshot, nil
}

func (f *fakeRunToolService) GetRunStatus(runID string) (contracts.RunSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	snapshot, ok := f.snapshots[runID]
	if !ok {
		return contracts.RunSnapshot{}, &contracts.RunToolError{Code: "run_not_found", Message: "run not found"}
	}
	return snapshot, nil
}

func (f *fakeRunToolService) Interrupt(_ context.Context, req runtimetypes.InterruptCommand) (runtimetypes.InterruptResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.interrupts = append(f.interrupts, req)
	snapshot := f.snapshots[req.RunID]
	snapshot.Status = "interrupted"
	f.snapshots[req.RunID] = snapshot
	return runtimetypes.InterruptResult{Accepted: true, Status: "accepted", RunID: req.RunID}, nil
}

func (f *fakeRunToolService) Steer(_ context.Context, req runtimetypes.SteerCommand) (runtimetypes.SteerResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.steerCalls = append(f.steerCalls, req)
	result := f.steerResult
	result.RunID, result.SteerID = req.RunID, req.SteerID
	return result, f.steerErr
}

func runToolExecContext(subject string, toolID string) *contracts.ExecutionContext {
	session := contracts.QuerySession{
		RunID:    "parent-run",
		ChatID:   "parent-chat",
		AgentKey: "zenmi",
		Subject:  subject,
		RunOwner: contracts.AgentRunOwner("zenmi"),
	}
	return &contracts.ExecutionContext{Session: session, CurrentToolID: toolID, CurrentToolName: "chat_send"}
}
