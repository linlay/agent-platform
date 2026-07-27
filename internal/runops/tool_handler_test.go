package runops

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

type fakeRunToolService struct {
	mu         sync.Mutex
	starts     int
	snapshots  map[string]contracts.RunSnapshot
	interrupts []api.InterruptRequest
}

func newFakeRunToolService() *fakeRunToolService {
	return &fakeRunToolService{snapshots: map[string]contracts.RunSnapshot{}}
}

func (f *fakeRunToolService) StartRun(_ context.Context, req contracts.RunStartRequest) (contracts.RunSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	runID := fmt.Sprintf("target-%d", f.starts)
	snapshot := contracts.RunSnapshot{
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

func (f *fakeRunToolService) GetRunStatus(runID string) (contracts.RunSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	snapshot, ok := f.snapshots[runID]
	if !ok {
		return contracts.RunSnapshot{}, &contracts.RunToolError{Code: "run_not_found", Message: "run not found"}
	}
	return snapshot, nil
}

func (f *fakeRunToolService) InterruptRun(req api.InterruptRequest) (api.InterruptResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.interrupts = append(f.interrupts, req)
	snapshot := f.snapshots[req.RunID]
	snapshot.Status = "interrupted"
	f.snapshots[req.RunID] = snapshot
	return api.InterruptResponse{Accepted: true, Status: "accepted", RunID: req.RunID}, nil
}

func runToolExecContext(subject string, toolID string) *contracts.ExecutionContext {
	session := contracts.QuerySession{
		RunID:    "parent-run",
		ChatID:   "parent-chat",
		AgentKey: "zenmi",
		Subject:  subject,
		RunOwner: contracts.AgentRunOwner("zenmi", ""),
	}
	return &contracts.ExecutionContext{Session: session, CurrentToolID: toolID, CurrentToolName: QueryToolName}
}

func TestRunQueryIsIdempotentPerParentRunAndToolID(t *testing.T) {
	service := newFakeRunToolService()
	runs := contracts.NewInMemoryRunManager()
	_, _, _ = runs.Register(context.Background(), contracts.QuerySession{
		RunID: "parent-run", ChatID: "parent-chat", AgentKey: "zenmi", RunOwner: contracts.AgentRunOwner("zenmi", ""),
	})
	handler := NewToolHandler(service, runs)
	args := map[string]any{"agentKey": "webOperator", "message": "search"}

	first, err := handler.Invoke(context.Background(), QueryToolName, args, runToolExecContext("alice", "tool-1"))
	if err != nil || first.Error != "" {
		t.Fatalf("first query failed: result=%#v err=%v", first, err)
	}
	second, err := handler.Invoke(context.Background(), QueryToolName, args, runToolExecContext("alice", "tool-1"))
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

	third, _ := handler.Invoke(context.Background(), QueryToolName, args, runToolExecContext("alice", "tool-2"))
	if third.Error != "" || service.starts != 2 {
		t.Fatalf("different toolId should create another run: result=%#v starts=%d", third, service.starts)
	}
}

func TestRunAllowsSelfTargetAndRejectsChainingAndUnownedRuns(t *testing.T) {
	service := newFakeRunToolService()
	service.snapshots["external-run"] = contracts.RunSnapshot{RunID: "external-run", ChatID: "external-chat", AgentKey: "other", Status: "running"}
	handler := NewToolHandler(service, nil)

	self, _ := handler.Invoke(context.Background(), QueryToolName, map[string]any{
		"agentKey": "zenmi", "message": "loop",
	}, runToolExecContext("alice", "tool-self"))
	if self.Error != "" || self.Structured["accepted"] != true {
		t.Fatalf("self target should be accepted: %#v", self)
	}

	chainedCtx := runToolExecContext("alice", "tool-chain")
	chainedCtx.Session.RunQueryOrigin = &contracts.RunQueryOrigin{CallerAgentKey: "zenmi"}
	chained, _ := handler.Invoke(context.Background(), QueryToolName, map[string]any{
		"agentKey": "webOperator", "message": "loop",
	}, chainedCtx)
	if chained.Error != "run_chaining_not_allowed" {
		t.Fatalf("chaining error = %q", chained.Error)
	}

	unowned, _ := handler.Invoke(context.Background(), StatusToolName, map[string]any{
		"runId": "external-run",
	}, runToolExecContext("alice", "tool-status"))
	if unowned.Error != "run_not_owned" {
		t.Fatalf("unowned error = %q", unowned.Error)
	}
}

func TestRunToolsRejectUnsupportedCallers(t *testing.T) {
	handler := NewToolHandler(newFakeRunToolService(), nil)
	args := map[string]any{"agentKey": "webOperator", "message": "search"}

	noContext, _ := handler.Invoke(context.Background(), QueryToolName, args, nil)
	if noContext.Error != "run_context_required" {
		t.Fatalf("missing context error = %q", noContext.Error)
	}

	child := runToolExecContext("alice", "tool-child")
	child.Session.SubTaskID = "child-task"
	childResult, _ := handler.Invoke(context.Background(), QueryToolName, args, child)
	if childResult.Error != "run_caller_not_allowed" {
		t.Fatalf("child caller error = %q", childResult.Error)
	}

	teamMember := runToolExecContext("alice", "tool-team-member")
	teamMember.Session.TeamID = "research"
	teamMember.Session.RunOwner = contracts.TeamRunOwner("research", "member")
	teamMemberResult, _ := handler.Invoke(context.Background(), QueryToolName, args, teamMember)
	if teamMemberResult.Error != "run_caller_not_allowed" {
		t.Fatalf("Team member caller error = %q", teamMemberResult.Error)
	}

	coordinator := runToolExecContext("alice", "tool-coordinator")
	coordinator.Session.AgentKey = "__team_coordinator"
	coordinator.Session.TeamID = "research"
	coordinator.Session.RunOwner = contracts.TeamRunOwner("research", "__team_coordinator")
	coordinatorResult, _ := handler.Invoke(context.Background(), QueryToolName, args, coordinator)
	if coordinatorResult.Error != "run_caller_not_allowed" {
		t.Fatalf("Team coordinator caller error = %q", coordinatorResult.Error)
	}
}

func TestRunOwnershipIncludesSubject(t *testing.T) {
	service := newFakeRunToolService()
	handler := NewToolHandler(service, nil)
	started, _ := handler.Invoke(context.Background(), QueryToolName, map[string]any{
		"agentKey": "webOperator", "message": "search",
	}, runToolExecContext("alice", "tool-query"))
	runID := started.Structured["run"].(map[string]any)["runId"].(string)

	denied, _ := handler.Invoke(context.Background(), StatusToolName, map[string]any{
		"runId": runID,
	}, runToolExecContext("bob", "tool-status"))
	if denied.Error != "run_not_owned" {
		t.Fatalf("different subject error = %q", denied.Error)
	}
}

func TestRunQueryValidatesTargetAndMessage(t *testing.T) {
	service := newFakeRunToolService()
	handler := NewToolHandler(service, nil)
	execCtx := runToolExecContext("alice", "tool-query")

	for _, args := range []map[string]any{
		{"message": "missing target"},
		{"agentKey": "writer"},
		{"agentKey": "writer", "teamId": "research", "message": "ambiguous"},
	} {
		result, _ := handler.Invoke(context.Background(), QueryToolName, args, execCtx)
		if result.Error != "invalid_request" {
			t.Fatalf("args %#v error = %q, want invalid_request", args, result.Error)
		}
	}

	team, _ := handler.Invoke(context.Background(), QueryToolName, map[string]any{
		"teamId": "research", "message": "review",
	}, execCtx)
	if team.Error != "" || team.Structured["action"] != "query" {
		t.Fatalf("team query failed: %#v", team)
	}
}

func TestGetRunStatusAndInterrupt(t *testing.T) {
	service := newFakeRunToolService()
	handler := NewToolHandler(service, nil)
	execCtx := runToolExecContext("alice", "tool-query")
	started, _ := handler.Invoke(context.Background(), QueryToolName, map[string]any{
		"agentKey": "webOperator", "message": "search",
	}, execCtx)
	runID := started.Structured["run"].(map[string]any)["runId"].(string)

	status, _ := handler.Invoke(context.Background(), StatusToolName, map[string]any{"runId": runID}, runToolExecContext("alice", "tool-status"))
	if status.Error != "" || status.Structured["action"] != "status" {
		t.Fatalf("status failed: %#v", status)
	}
	missingStatus, _ := handler.Invoke(context.Background(), StatusToolName, map[string]any{}, runToolExecContext("alice", "tool-status-missing"))
	if missingStatus.Error != "invalid_request" {
		t.Fatalf("missing status runId error = %q", missingStatus.Error)
	}
	notFound, _ := handler.Invoke(context.Background(), StatusToolName, map[string]any{"runId": "missing"}, runToolExecContext("alice", "tool-status-not-found"))
	if notFound.Error != "run_not_found" {
		t.Fatalf("missing run error = %q", notFound.Error)
	}

	interrupted, _ := handler.Invoke(context.Background(), InterruptToolName, map[string]any{
		"runId": runID, "message": "stop now",
	}, runToolExecContext("alice", "tool-interrupt"))
	if interrupted.Error != "" || interrupted.Structured["action"] != "interrupt" {
		t.Fatalf("interrupt failed: %#v", interrupted)
	}
	if len(service.interrupts) != 1 || service.interrupts[0].Message != "stop now" || service.interrupts[0].RunID != runID {
		t.Fatalf("unexpected interrupt requests: %#v", service.interrupts)
	}
	missingInterrupt, _ := handler.Invoke(context.Background(), InterruptToolName, map[string]any{}, runToolExecContext("alice", "tool-interrupt-missing"))
	if missingInterrupt.Error != "invalid_request" {
		t.Fatalf("missing interrupt runId error = %q", missingInterrupt.Error)
	}
}
