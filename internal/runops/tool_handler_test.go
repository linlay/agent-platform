package runops

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"agent-platform/internal/contracts"
	"agent-platform/internal/runtime/runstate"
	runtimetypes "agent-platform/internal/runtime/types"
)

type fakeRunToolService struct {
	mu          sync.Mutex
	parentLevel string
	startErr    error
	starts      int
	requests    []contracts.RunStartRequest
	snapshots   map[string]contracts.RunSnapshot
	interrupts  []runtimetypes.InterruptCommand
}

func newFakeRunToolService() *fakeRunToolService {
	return &fakeRunToolService{snapshots: map[string]contracts.RunSnapshot{}}
}

func (f *fakeRunToolService) PrepareRunStart(_ context.Context, req contracts.RunStartRequest) (contracts.RunStartPlan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	parent, _ := contracts.NormalizeAccessLevel(f.parentLevel)
	plan := contracts.RunStartPlan{RequestedAccessLevel: req.AccessLevel, ParentAccessLevel: parent, ParentAccessVersion: 1, TargetName: "Target"}
	plan.AccessLevel, plan.RequiresApproval = contracts.ResolveRunStartAccessLevel(req.AccessLevel, parent)
	plan.RequestDigest = contracts.RunStartRequestDigest(req)
	plan.ApprovalDigest = contracts.RunStartApprovalDigest(plan.RequestDigest, parent, 1, plan.AccessLevel)
	return plan, nil
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

func runToolExecContext(subject string, toolID string) *contracts.ExecutionContext {
	session := contracts.QuerySession{
		RunID:    "parent-run",
		ChatID:   "parent-chat",
		AgentKey: "zenmi",
		Subject:  subject,
		RunOwner: contracts.AgentRunOwner("zenmi"),
	}
	return &contracts.ExecutionContext{Session: session, CurrentToolID: toolID, CurrentToolName: StartToolName}
}

func TestChatStartIsIdempotentPerParentRunAndToolID(t *testing.T) {
	service := newFakeRunToolService()
	runs := runstate.NewManager()
	_, _, _ = runs.Register(context.Background(), contracts.QuerySession{
		RunID: "parent-run", ChatID: "parent-chat", AgentKey: "zenmi", RunOwner: contracts.AgentRunOwner("zenmi"),
	})
	handler := NewToolHandler(service, runs)
	args := map[string]any{"agentKey": "webOperator", "message": "search"}

	first, err := handler.Invoke(context.Background(), StartToolName, args, runToolExecContext("alice", "tool-1"))
	if err != nil || first.Error != "" {
		t.Fatalf("first query failed: result=%#v err=%v", first, err)
	}
	second, err := handler.Invoke(context.Background(), StartToolName, args, runToolExecContext("alice", "tool-1"))
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

	third, _ := handler.Invoke(context.Background(), StartToolName, args, runToolExecContext("alice", "tool-2"))
	if third.Error != "" || service.starts != 2 {
		t.Fatalf("different toolId should create another run: result=%#v starts=%d", third, service.starts)
	}
}

func TestRunAllowsSelfTargetAndRejectsChainingAndUnownedRuns(t *testing.T) {
	service := newFakeRunToolService()
	service.snapshots["external-run"] = contracts.RunSnapshot{RunID: "external-run", ChatID: "external-chat", AgentKey: "other", Status: "running"}
	handler := NewToolHandler(service, nil)

	self, _ := handler.Invoke(context.Background(), StartToolName, map[string]any{
		"agentKey": "zenmi", "message": "loop",
	}, runToolExecContext("alice", "tool-self"))
	if self.Error != "" || self.Structured["accepted"] != true {
		t.Fatalf("self target should be accepted: %#v", self)
	}

	chainedCtx := runToolExecContext("alice", "tool-chain")
	chainedCtx.Session.RunOrigin = &contracts.RunOrigin{AgentKey: "zenmi"}
	chained, _ := handler.Invoke(context.Background(), StartToolName, map[string]any{
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

	noContext, _ := handler.Invoke(context.Background(), StartToolName, args, nil)
	if noContext.Error != "run_context_required" {
		t.Fatalf("missing context error = %q", noContext.Error)
	}

	child := runToolExecContext("alice", "tool-child")
	child.Session.SubTaskID = "child-task"
	childResult, _ := handler.Invoke(context.Background(), StartToolName, args, child)
	if childResult.Error != "run_caller_not_allowed" {
		t.Fatalf("child caller error = %q", childResult.Error)
	}

	teamMember := runToolExecContext("alice", "tool-team-member")

	teamMember.Session.RunOwner = contracts.AgentRunOwner("member")
	teamMemberResult, _ := handler.Invoke(context.Background(), StartToolName, args, teamMember)
	if teamMemberResult.Error != "run_caller_not_allowed" {
		t.Fatalf("Team member caller error = %q", teamMemberResult.Error)
	}

	coordinator := runToolExecContext("alice", "tool-coordinator")
	coordinator.Session.AgentKey = "research"
	coordinator.Session.Mode = "TEAM"

	coordinator.Session.RunOwner = contracts.AgentRunOwner("research")
	coordinatorResult, _ := handler.Invoke(context.Background(), StartToolName, args, coordinator)
	if coordinatorResult.Error != "" {
		t.Fatalf("Team coordinator caller error = %q", coordinatorResult.Error)
	}
}

func TestRunOwnershipIncludesSubject(t *testing.T) {
	service := newFakeRunToolService()
	handler := NewToolHandler(service, nil)
	started, _ := handler.Invoke(context.Background(), StartToolName, map[string]any{
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

func TestChatStartValidatesTargetAndMessage(t *testing.T) {
	service := newFakeRunToolService()
	handler := NewToolHandler(service, nil)
	execCtx := runToolExecContext("alice", "tool-query")

	for _, args := range []map[string]any{
		{"agentKey": " ", "message": "task"},
		{"agentKey": "writer"},
	} {
		result, _ := handler.Invoke(context.Background(), StartToolName, args, execCtx)
		if result.Error != "invalid_request" {
			t.Fatalf("args %#v error = %q, want invalid_request", args, result.Error)
		}
	}

}

func TestGetRunStatusAndInterrupt(t *testing.T) {
	service := newFakeRunToolService()
	handler := NewToolHandler(service, nil)
	execCtx := runToolExecContext("alice", "tool-query")
	started, _ := handler.Invoke(context.Background(), StartToolName, map[string]any{
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

func TestChatStartEscalationRequiresManualOneShotApproval(t *testing.T) {
	for _, tc := range []struct {
		parent, requested string
		review            bool
	}{
		{"default", "", false}, {"default", "default", false}, {"default", "auto_approve", true}, {"default", "full_access", true},
		{"auto_approve", "", false}, {"auto_approve", "default", false}, {"auto_approve", "auto_approve", false}, {"auto_approve", "full_access", true},
		{"full_access", "", false}, {"full_access", "default", false}, {"full_access", "auto_approve", false}, {"full_access", "full_access", false},
	} {
		t.Run(tc.parent+"/"+tc.requested, func(t *testing.T) {
			service := newFakeRunToolService()
			service.parentLevel = tc.parent
			handler := NewToolHandler(service, nil)
			args := map[string]any{"agentKey": "worker", "message": "do it"}
			if tc.requested != "" {
				args["accessLevel"] = tc.requested
			}
			execCtx := runToolExecContext("alice", "tool-1")
			approval, err := handler.PrepareToolApproval(context.Background(), StartToolName, args, execCtx)
			if err != nil || (approval != nil) != tc.review {
				t.Fatalf("approval=%#v err=%v", approval, err)
			}
			if !tc.review {
				if _, err := handler.Invoke(context.Background(), StartToolName, args, execCtx); err != nil || service.requests[0].Review != nil {
					t.Fatalf("unreviewed start carried a review: %#v %v", service.requests, err)
				}
				return
			}
			if approval.AllowAutoApprove || approval.Fingerprint == "" {
				t.Fatalf("escalation must be manual and exact: %#v", approval)
			}
			if approval.Form["message"] != "do it" || approval.Form["parentAccessLevel"] != tc.parent || approval.Form["accessLevel"] != tc.requested {
				t.Fatalf("review does not show the approved object: %#v", approval.Form)
			}
			if service.starts != 0 {
				t.Fatal("review started a run")
			}
			if _, err := handler.Invoke(context.Background(), StartToolName, args, execCtx); err != nil {
				t.Fatal(err)
			}
			review := service.requests[0].Review
			if review == nil || review.ParentAccessLevel != tc.parent || review.ParentAccessVersion != 1 || review.ApprovalDigest == "" {
				t.Fatalf("frozen baseline was not forwarded: %#v", review)
			}
			// The receipt exists only in the trusted execution context and is one-shot.
			if review.Consume(review.ApprovalDigest) {
				t.Fatal("consumed an approval nobody granted")
			}
			execCtx.ToolApprovals = map[string]bool{approval.Fingerprint: true}
			if review.Consume("another-digest") || !review.Consume(review.ApprovalDigest) || review.Consume(review.ApprovalDigest) {
				t.Fatal("approval must be bound to its digest and consumed once")
			}
		})
	}
}

func TestChatStartPlannerIgnoresOtherToolsAndInvalidRequests(t *testing.T) {
	service := newFakeRunToolService()
	handler := NewToolHandler(service, nil)
	for _, tc := range []struct {
		tool string
		args map[string]any
		exec *contracts.ExecutionContext
	}{
		{StatusToolName, map[string]any{"runId": "x"}, runToolExecContext("alice", "tool")},
		{StartToolName, map[string]any{"agentKey": "worker", "accessLevel": "full_access"}, runToolExecContext("alice", "tool")},
		{StartToolName, map[string]any{"agentKey": "worker", "message": "m", "accessLevel": "full_access", "approved": true}, runToolExecContext("alice", "tool")},
		{StartToolName, map[string]any{"agentKey": "worker", "message": "m", "accessLevel": "full_access"}, nil},
	} {
		if approval, err := handler.PrepareToolApproval(context.Background(), tc.tool, tc.args, tc.exec); approval != nil || err != nil {
			t.Fatalf("%s %v: %#v %v", tc.tool, tc.args, approval, err)
		}
	}
	// Forged authorization fields stay unknown arguments and start nothing.
	result, _ := handler.Invoke(context.Background(), StartToolName, map[string]any{"agentKey": "worker", "message": "m", "accessLevel": "full_access", "approved": true}, runToolExecContext("alice", "tool"))
	if result.Error != "unknown_argument" || service.starts != 0 {
		t.Fatalf("result=%#v starts=%d", result, service.starts)
	}
}

func TestChatStartIdempotencyDistinguishesOutcomes(t *testing.T) {
	args := map[string]any{"agentKey": "worker", "message": "do it"}
	t.Run("conflicting arguments", func(t *testing.T) {
		service := newFakeRunToolService()
		handler := NewToolHandler(service, nil)
		execCtx := runToolExecContext("alice", "tool-1")
		if result, _ := handler.Invoke(context.Background(), StartToolName, args, execCtx); result.Error != "" {
			t.Fatalf("first: %#v", result)
		}
		result, _ := handler.Invoke(context.Background(), StartToolName, map[string]any{"agentKey": "worker", "message": "something else"}, execCtx)
		if result.Error != "idempotency_conflict" || result.Structured["executionState"] != "not_started" || service.starts != 1 {
			t.Fatalf("result=%#v starts=%d", result, service.starts)
		}
		// Omitted and explicit default are different requests.
		result, _ = handler.Invoke(context.Background(), StartToolName, map[string]any{"agentKey": "worker", "message": "do it", "accessLevel": "default"}, execCtx)
		if result.Error != "idempotency_conflict" {
			t.Fatalf("result=%#v", result)
		}
	})
	t.Run("not started may be retried", func(t *testing.T) {
		service := newFakeRunToolService()
		service.startErr = &contracts.RunToolError{Code: "run_start_review_stale", Message: "stale", ExecutionState: "not_started", Retryable: true}
		handler := NewToolHandler(service, nil)
		execCtx := runToolExecContext("alice", "tool-1")
		result, _ := handler.Invoke(context.Background(), StartToolName, args, execCtx)
		if result.Error != "run_start_review_stale" || result.Structured["executionState"] != "not_started" || result.Structured["retryable"] != true {
			t.Fatalf("result=%#v", result)
		}
		service.startErr = nil
		if result, _ := handler.Invoke(context.Background(), StartToolName, args, execCtx); result.Error != "" || service.starts != 2 {
			t.Fatalf("retry: %#v starts=%d", result, service.starts)
		}
	})
	t.Run("unknown outcome is reconciled, never restarted", func(t *testing.T) {
		service := newFakeRunToolService()
		service.startErr = &contracts.RunToolError{Code: "run_start_outcome_unknown", Message: "unknown", ExecutionState: "unknown", RunID: "maybe-run", ChatID: "maybe-chat"}
		handler := NewToolHandler(service, nil)
		execCtx := runToolExecContext("alice", "tool-1")
		result, _ := handler.Invoke(context.Background(), StartToolName, args, execCtx)
		if result.Error != "run_start_outcome_unknown" || result.Structured["executionState"] != "unknown" {
			t.Fatalf("result=%#v", result)
		}
		service.startErr = nil
		result, _ = handler.Invoke(context.Background(), StartToolName, args, execCtx)
		run, _ := result.Structured["run"].(map[string]any)
		if result.Error != "" || run["runId"] != "maybe-run" || service.starts != 1 {
			t.Fatalf("retry restarted or lost the run: %#v starts=%d", result, service.starts)
		}
	})
}
