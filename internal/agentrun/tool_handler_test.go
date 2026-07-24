package agentrun

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

type fakeAgentRunService struct {
	mu        sync.Mutex
	starts    int
	snapshots map[string]contracts.AgentRunSnapshot
}

func newFakeAgentRunService() *fakeAgentRunService {
	return &fakeAgentRunService{snapshots: map[string]contracts.AgentRunSnapshot{}}
}

func (f *fakeAgentRunService) StartAgentRun(_ context.Context, req contracts.AgentRunStartRequest) (contracts.AgentRunSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	runID := fmt.Sprintf("target-%d", f.starts)
	snapshot := contracts.AgentRunSnapshot{
		RunID:     runID,
		ChatID:    "chat-" + runID,
		AgentKey:  req.AgentKey,
		TeamID:    req.TeamID,
		Status:    "running",
		StartedAt: 1700000000000,
		Origin:    &req.Origin,
	}
	f.snapshots[runID] = snapshot
	return snapshot, nil
}

func (f *fakeAgentRunService) AgentRunStatus(runID string) (contracts.AgentRunSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	snapshot, ok := f.snapshots[runID]
	if !ok {
		return contracts.AgentRunSnapshot{}, &contracts.AgentRunError{Code: "run_not_found", Message: "run not found"}
	}
	return snapshot, nil
}

func (f *fakeAgentRunService) AgentRunSubmitQuestion(req api.SubmitRequest) (api.SubmitResponse, error) {
	return api.SubmitResponse{Accepted: true, Status: "accepted", RunID: req.RunID, AwaitingID: req.AwaitingID}, nil
}

func (f *fakeAgentRunService) AgentRunSteer(req api.SteerRequest) (api.SteerResponse, error) {
	return api.SteerResponse{Accepted: true, Status: "accepted", RunID: req.RunID, SteerID: "steer-1"}, nil
}

func (f *fakeAgentRunService) AgentRunInterrupt(req api.InterruptRequest) (api.InterruptResponse, error) {
	return api.InterruptResponse{Accepted: true, Status: "accepted", RunID: req.RunID}, nil
}

func agentRunExecContext(subject string, toolID string) *contracts.ExecutionContext {
	session := contracts.QuerySession{
		RunID:    "parent-run",
		ChatID:   "parent-chat",
		AgentKey: "zenmi",
		Subject:  subject,
		RunOwner: contracts.AgentRunOwner("zenmi", ""),
	}
	return &contracts.ExecutionContext{Session: session, CurrentToolID: toolID, CurrentToolName: ToolName}
}

func TestAgentRunQueryIsIdempotentPerParentRunAndToolID(t *testing.T) {
	service := newFakeAgentRunService()
	runs := contracts.NewInMemoryRunManager()
	_, _, _ = runs.Register(context.Background(), contracts.QuerySession{
		RunID: "parent-run", ChatID: "parent-chat", AgentKey: "zenmi", RunOwner: contracts.AgentRunOwner("zenmi", ""),
	})
	handler := NewToolHandler(service, runs)
	args := map[string]any{"action": "query", "agentKey": "webOperator", "message": "search"}

	first, err := handler.Invoke(context.Background(), ToolName, args, agentRunExecContext("alice", "tool-1"))
	if err != nil || first.Error != "" {
		t.Fatalf("first query failed: result=%#v err=%v", first, err)
	}
	second, err := handler.Invoke(context.Background(), ToolName, args, agentRunExecContext("alice", "tool-1"))
	if err != nil || second.Error != "" {
		t.Fatalf("idempotent retry failed: result=%#v err=%v", second, err)
	}
	if service.starts != 1 {
		t.Fatalf("start count = %d, want 1", service.starts)
	}
	firstRun := first.Structured["run"].(map[string]any)["runId"]
	secondRun := second.Structured["run"].(map[string]any)["runId"]
	if firstRun != secondRun {
		t.Fatalf("retry changed run: first=%v second=%v", firstRun, secondRun)
	}

	third, _ := handler.Invoke(context.Background(), ToolName, args, agentRunExecContext("alice", "tool-2"))
	if third.Error != "" || service.starts != 2 {
		t.Fatalf("different toolId should create another run: result=%#v starts=%d", third, service.starts)
	}
}

func TestAgentRunAllowsSelfTargetAndRejectsChainingAndUnownedRuns(t *testing.T) {
	service := newFakeAgentRunService()
	service.snapshots["external-run"] = contracts.AgentRunSnapshot{RunID: "external-run", ChatID: "external-chat", AgentKey: "other", Status: "running"}
	handler := NewToolHandler(service, nil)

	self, _ := handler.Invoke(context.Background(), ToolName, map[string]any{
		"action": "query", "agentKey": "zenmi", "message": "loop",
	}, agentRunExecContext("alice", "tool-self"))
	if self.Error != "" || self.Structured["accepted"] != true {
		t.Fatalf("self target should be accepted: %#v", self)
	}

	chainedCtx := agentRunExecContext("alice", "tool-chain")
	chainedCtx.Session.AgentRunOrigin = &contracts.AgentRunOrigin{CallerAgentKey: "zenmi"}
	chained, _ := handler.Invoke(context.Background(), ToolName, map[string]any{
		"action": "query", "agentKey": "webOperator", "message": "loop",
	}, chainedCtx)
	if chained.Error != "agent_run_chaining_not_allowed" {
		t.Fatalf("chaining error = %q", chained.Error)
	}

	unowned, _ := handler.Invoke(context.Background(), ToolName, map[string]any{
		"action": "status", "runId": "external-run",
	}, agentRunExecContext("alice", "tool-status"))
	if unowned.Error != "run_not_owned" {
		t.Fatalf("unowned error = %q", unowned.Error)
	}
}

func TestAgentRunOwnershipIncludesSubject(t *testing.T) {
	service := newFakeAgentRunService()
	handler := NewToolHandler(service, nil)
	started, _ := handler.Invoke(context.Background(), ToolName, map[string]any{
		"action": "query", "agentKey": "webOperator", "message": "search",
	}, agentRunExecContext("alice", "tool-query"))
	runID := started.Structured["run"].(map[string]any)["runId"].(string)

	denied, _ := handler.Invoke(context.Background(), ToolName, map[string]any{
		"action": "status", "runId": runID,
	}, agentRunExecContext("bob", "tool-status"))
	if denied.Error != "run_not_owned" {
		t.Fatalf("different subject error = %q", denied.Error)
	}
}
