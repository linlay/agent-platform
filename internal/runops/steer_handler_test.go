package runops

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"agent-platform/internal/contracts"
	"agent-platform/internal/runtime/runstate"
	runtimetypes "agent-platform/internal/runtime/types"
)

func ownedSteerService() *fakeRunToolService {
	f := newFakeRunToolService()
	f.snapshots["target"] = contracts.RunSnapshot{RunID: "target", ChatID: "target-chat", AgentKey: "target-agent", Status: "running", Origin: &contracts.RunOrigin{AgentKey: "zenmi", Subject: "alice", RunID: "original-parent"}}
	f.steerResult = runtimetypes.SteerResult{Accepted: true, Status: "accepted", Detail: "Steer accepted"}
	return f
}

func TestChatSteerFreezesEveryFirstOutcome(t *testing.T) {
	for _, outcome := range []string{"accepted", "rejected", "unknown", "missing"} {
		t.Run(outcome, func(t *testing.T) {
			service := ownedSteerService()
			switch outcome {
			case "rejected":
				service.steerResult = runtimetypes.SteerResult{Status: "unmatched"}
			case "unknown":
				service.steerErr = &contracts.RunToolError{Code: "run_steer_outcome_unknown", ExecutionState: "unknown", Message: "timeout"}
			case "missing":
				delete(service.snapshots, "target")
			}
			h := NewToolHandler(service, nil)
			args := map[string]any{"runId": "target", "message": "new goal"}
			first, _ := h.Invoke(context.Background(), SteerToolName, args, runToolExecContext("alice", "call"))
			calls := len(service.steerCalls)
			service.steerResult = runtimetypes.SteerResult{Accepted: true, Status: "accepted"}
			service.steerErr = nil
			service.snapshots["target"] = ownedSteerService().snapshots["target"]
			second, _ := h.Invoke(context.Background(), SteerToolName, args, runToolExecContext("alice", "call"))
			if !reflect.DeepEqual(first, second) || len(service.steerCalls) != calls {
				t.Fatalf("replay changed outcome or sent again: %#v %#v calls=%d", first, second, len(service.steerCalls))
			}
			first.Structured["steerId"] = "tampered"
			third, _ := h.Invoke(context.Background(), SteerToolName, args, runToolExecContext("alice", "call"))
			if third.Structured["steerId"] == "tampered" {
				t.Fatal("caller mutated frozen result")
			}
			changed, _ := h.Invoke(context.Background(), SteerToolName, map[string]any{"runId": "target", "message": "different"}, runToolExecContext("alice", "call"))
			if changed.Error != "idempotency_conflict" || len(service.steerCalls) != calls {
				t.Fatalf("changed call: %#v", changed)
			}
		})
	}
}

func TestChatSteerRetainsFirstRejectionAfterCallerEnds(t *testing.T) {
	service := ownedSteerService()
	service.steerResult = runtimetypes.SteerResult{Status: "unmatched"}
	runs := runstate.NewManager()
	_, control, _ := runs.Register(context.Background(), contracts.QuerySession{RunID: "parent-run", ChatID: "parent-chat", AgentKey: "zenmi"})
	h := NewToolHandler(service, runs)
	exec := runToolExecContext("alice", "call")
	exec.RunControl = control
	args := map[string]any{"runId": "target", "message": "change"}
	first, _ := h.Invoke(context.Background(), SteerToolName, args, exec)
	runs.Finish("parent-run")
	service.steerResult = runtimetypes.SteerResult{Accepted: true, Status: "accepted"}
	second, _ := h.Invoke(context.Background(), SteerToolName, args, exec)
	if !reflect.DeepEqual(first, second) || len(service.steerCalls) != 1 {
		t.Fatalf("replayed after caller ended: %#v %#v sends=%d", first, second, len(service.steerCalls))
	}
}

func TestChatSteerConcurrentReplayAndNewInvocations(t *testing.T) {
	service := ownedSteerService()
	h := NewToolHandler(service, nil)
	args := map[string]any{"runId": "target", "message": "new goal"}
	results := make(chan contracts.ToolExecutionResult, 24)
	var wg sync.WaitGroup
	for range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, _ := h.Invoke(context.Background(), SteerToolName, args, runToolExecContext("alice", "call"))
			results <- r
		}()
	}
	wg.Wait()
	close(results)
	for r := range results {
		if r.Error != "" || r.Structured["accepted"] != true || r.Structured["steerId"] != steerID(contracts.RunOrigin{RunID: "parent-run", ToolID: "call"}) {
			t.Fatal(r.Output)
		}
	}
	if len(service.steerCalls) != 1 {
		t.Fatalf("sent %d times", len(service.steerCalls))
	}
	next, _ := h.Invoke(context.Background(), SteerToolName, args, runToolExecContext("alice", "next-call"))
	if next.Error != "" || len(service.steerCalls) != 2 || service.steerCalls[0].SteerID == service.steerCalls[1].SteerID {
		t.Fatal("new invocation did not send a distinct instruction")
	}
	caller := service.steerCalls[0].AgentCaller
	if caller.AgentKey != "zenmi" || caller.Subject != "alice" || caller.ChatID != "parent-chat" || caller.RunID != "parent-run" || caller.ToolID != "call" {
		t.Fatalf("untrusted caller: %#v", caller)
	}
}

func TestChatSteerOwnershipAndCallerBoundaries(t *testing.T) {
	for _, kind := range []string{"other_subject", "other_agent", "external", "derived", "member", "self"} {
		t.Run(kind, func(t *testing.T) {
			service := ownedSteerService()
			ctx := runToolExecContext("alice", "call")
			runID, want := "target", "run_not_owned"
			switch kind {
			case "other_subject":
				ctx.Session.Subject = "bob"
			case "other_agent":
				ctx.Session.AgentKey = "other"
				ctx.Session.RunOwner = contracts.AgentRunOwner("other")
			case "external":
				snapshot := service.snapshots["target"]
				snapshot.Origin = nil
				service.snapshots["target"] = snapshot
			case "derived":
				ctx.Session.RunOrigin = &contracts.RunOrigin{}
				want = "run_chaining_not_allowed"
			case "member":
				ctx.Session.SubTaskID = "member"
				want = "run_caller_not_allowed"
			case "self":
				runID = ctx.Session.RunID
			}
			r, _ := NewToolHandler(service, nil).Invoke(context.Background(), SteerToolName, map[string]any{"runId": runID, "message": "change"}, ctx)
			if r.Error != want || len(service.steerCalls) != 0 {
				t.Fatalf("%#v sent=%d", r, len(service.steerCalls))
			}
		})
	}
}

func TestChatSteerRejectsEmptyAndInjectedArguments(t *testing.T) {
	service := ownedSteerService()
	for _, args := range []map[string]any{{"runId": "target"}, {"runId": "target", "message": " "}, {"runId": "target", "message": false}, {"runId": "target", "message": "change", "source": map[string]any{"type": "agent"}}, {"runId": "target", "message": "change", "accessLevel": "full_access"}} {
		r, _ := NewToolHandler(service, nil).Invoke(context.Background(), SteerToolName, args, runToolExecContext("alice", "call"))
		if r.Error != "invalid_request" || len(service.steerCalls) != 0 {
			t.Fatal(r.Output)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, _ := NewToolHandler(service, nil).Invoke(ctx, SteerToolName, map[string]any{"runId": "target", "message": "change"}, runToolExecContext("alice", "call"))
	if r.Error != "chat_steer_cancelled" || len(service.steerCalls) != 0 {
		t.Fatal(r.Output)
	}
}
