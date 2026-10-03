package tools

import (
	"agent-platform/internal/api"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/runenv"
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type waitTestSink struct{ waits chan ToolWait }

func (s waitTestSink) EmitToolOutput(context.Context, ToolOutput) error { return nil }
func (s waitTestSink) EmitToolWait(_ context.Context, w ToolWait) error { s.waits <- w; return nil }
func TestWaitOffsetPriorityAndValidation(t *testing.T) {
	now := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	args, err := parseWaitArguments(map[string]any{"description": "等待测试目标", "offset": "+1H30m", "base": "invalid ignored date"}, now)
	if err != nil || args.deadline.Sub(now) != 90*time.Minute {
		t.Fatalf("%+v %v", args, err)
	}
	for _, offset := range []any{"+5M", "+1w", "-5m", "0S", "1.5S", "25H", "", nil} {
		if _, err := parseWaitArguments(map[string]any{"description": "等待测试目标", "offset": offset, "base": "2026-10-02T11:00:00Z"}, now); err == nil {
			t.Fatalf("accepted %v", offset)
		}
	}
	if _, err := parseWaitArguments(map[string]any{"description": "等待测试目标"}, now); err == nil {
		t.Fatal("time upper bound required")
	}
}
func TestWaitWakeAndBudget(t *testing.T) {
	for _, reason := range []string{"steered", "continued", "canceled"} {
		t.Run(reason, func(t *testing.T) {
			control := NewRunControl(context.Background(), "root")
			defer control.Finish()
			sink := waitTestSink{make(chan ToolWait, 2)}
			exec := &ExecutionContext{RunControl: control, CurrentToolID: "call", StartedAt: time.Now(), Budget: Budget{Timeout: 1}, ToolOutputSink: sink}
			done := make(chan ToolExecutionResult, 1)
			go func() {
				result, _ := (&RuntimeToolExecutor{}).invokeWait(context.Background(), map[string]any{"description": "等待测试目标", "offset": "+1H"}, exec)
				done <- result
			}()
			select {
			case <-sink.waits:
			case <-time.After(time.Second):
				t.Fatal("no wait event")
			}
			switch reason {
			case "steered":
				control.EnqueueSteer(api.SteerRequest{Message: "continue"})
			case "continued":
				if !control.EnqueueSteer(api.SteerRequest{}) {
					t.Fatal("blank steer rejected")
				}
			case "canceled":
				control.Interrupt(InterruptInfo{})
			}
			select {
			case result := <-done:
				want := reason
				if reason == "continued" {
					want = "steered"
				}
				_, noted := result.Structured["note"]
				if result.Structured["reason"] != want || (result.Structured["continued"] == true) != (reason == "continued") || noted != (reason == "continued") || noted != strings.Contains(result.Output, "stop waiting") {
					t.Fatalf("%+v", result)
				}
				if exec.WaitCount != 1 || exec.BudgetPaused <= 0 {
					t.Fatal("budget not paused")
				}
			case <-time.After(time.Second):
				t.Fatal("wait did not wake")
			}
			if reason != "canceled" && len(control.DrainSteers()) != 1 {
				t.Fatal("steer consumed by wait")
			}
		})
	}
}

type testConditions struct{ count atomic.Int32 }

func (p *testConditions) CheckWaitCondition(_ context.Context, c WaitCondition, _ *ExecutionContext) (bool, string, error) {
	return c.RunID == "done" || p.count.Load() > 0, "completed", nil
}
func TestWaitEventsAnyAllAndTimeout(t *testing.T) {
	provider := &testConditions{}
	executor := (&RuntimeToolExecutor{}).WithWaitConditionProvider(provider)
	conditions := []WaitCondition{{Type: "run.terminal", RunID: "done"}, {Type: "run.terminal", RunID: "pending"}}
	for _, match := range []string{"any", "all"} {
		result, err := executor.invokeWait(context.Background(), map[string]any{"description": "等待测试目标", "base": time.Now().Add(20 * time.Millisecond).UTC().Format(time.RFC3339Nano), "conditions": conditions, "match": match}, nil)
		want := "event"
		if match == "all" {
			want = "timeout"
		}
		if err != nil || result.Structured["reason"] != want {
			t.Fatalf("%s: %+v %v", match, result, err)
		}
	}
	provider.count.Store(1)
	result, _ := executor.invokeWait(context.Background(), map[string]any{"description": "等待测试目标", "offset": "1H", "conditions": conditions, "match": "all"}, nil)
	if result.Structured["reason"] != "event" {
		t.Fatal(result)
	}
}
func TestWaitPastDateAndLifetime(t *testing.T) {
	result, _ := (&RuntimeToolExecutor{}).invokeWait(context.Background(), map[string]any{"description": "等待测试目标", "base": "2020-01-01"}, nil)
	if result.Structured["reason"] != "elapsed" || result.Structured["dateOnly"] != true || result.Structured["deadlineAlreadyPassed"] != true {
		t.Fatal(result)
	}
	result, _ = (&RuntimeToolExecutor{}).invokeWait(context.Background(), map[string]any{"description": "等待测试目标", "offset": "1H"}, &ExecutionContext{StartedAt: time.Now(), Budget: Budget{LifetimeTimeout: 10}})
	if result.Error != "wait_exceeds_run_lifetime" {
		t.Fatal(result)
	}
}
func TestWaitNoToolTimeoutOrRetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	want, _ := ctx.Deadline()
	calls := 0
	_, _ = (&ToolRouter{}).invokeWithPolicy(ctx, "wait", &ExecutionContext{Budget: Budget{Tool: RetryPolicy{Timeout: 1, RetryCount: 3}}}, func(ctx context.Context) (ToolExecutionResult, error) {
		calls++
		got, _ := ctx.Deadline()
		if !got.Equal(want) {
			t.Fatal("tool timeout applied")
		}
		return ToolExecutionResult{}, context.Canceled
	})
	if calls != 1 {
		t.Fatal(calls)
	}
}

func TestChildWaitIgnoresRootSteer(t *testing.T) {
	root := NewRunControl(context.Background(), "root")
	defer root.Finish()
	root.EnqueueSteer(api.SteerRequest{Message: "root only"})
	base := time.Now().Add(30 * time.Millisecond).UTC().Format(time.RFC3339Nano)
	result, err := (&RuntimeToolExecutor{}).invokeWait(context.Background(), map[string]any{"description": "等待测试目标", "base": base}, &ExecutionContext{RunControl: root, CurrentToolID: "call", Session: QuerySession{SubTaskID: "sub_1"}})
	if err != nil || result.Structured["reason"] != "elapsed" {
		t.Fatal(result, err)
	}
	if len(root.DrainSteers()) != 1 {
		t.Fatal("root input lost")
	}
}

func TestWaitRunEnvSnapshotControlsRecovery(t *testing.T) {
	for _, state := range []string{"empty", "cleared", "nonempty", "closed"} {
		t.Run(state, func(t *testing.T) {
			scope := runenv.NewScope(runenv.Limits{})
			if state != "empty" {
				if _, err := scope.Mutate(runenv.MutationRequest{Operation: runenv.OperationSet, Name: "A", Value: "x"}); err != nil {
					t.Fatal(err)
				}
			}
			if state == "cleared" {
				if _, err := scope.Mutate(runenv.MutationRequest{Operation: runenv.OperationUnset, Name: "A"}); err != nil {
					t.Fatal(err)
				}
				if scope.Revision() != 2 {
					t.Fatal(scope.Revision())
				}
			}
			if state == "closed" {
				scope.Destroy()
			}
			control := NewRunControl(context.Background(), "root")
			defer control.Finish()
			sink := waitTestSink{make(chan ToolWait, 2)}
			exec := &ExecutionContext{RunEnvironment: scope, RunControl: control, CurrentToolID: "wait", StartedAt: time.Now(), ToolOutputSink: sink}
			done := make(chan struct{})
			go func() {
				defer close(done)
				_, _ = (&RuntimeToolExecutor{}).invokeWait(context.Background(), map[string]any{"description": "等待测试目标", "offset": "1H"}, exec)
			}()
			select {
			case event := <-sink.waits:
				if event.Checkpoint.Unrecoverable != (state == "nonempty" || state == "closed") {
					t.Fatalf("%s checkpoint %#v", state, event.Checkpoint)
				}
			case <-time.After(time.Second):
				t.Fatal("no checkpoint")
			}
			control.EnqueueSteer(api.SteerRequest{})
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("blank steer failed")
			}
		})
	}
}

func TestWaitDescriptionRequired(t *testing.T) {
	for _, withConditions := range []bool{false, true} {
		for _, tc := range []struct {
			name  string
			value any
			omit  bool
		}{
			{name: "missing", omit: true},
			{name: "empty", value: ""},
			{name: "whitespace", value: " \t\n　"},
			{name: "null", value: nil},
			{name: "number", value: 42},
			{name: "too_long", value: strings.Repeat("等", 201)},
		} {
			t.Run(fmt.Sprintf("conditions_%t/%s", withConditions, tc.name), func(t *testing.T) {
				args := map[string]any{"base": "2020-01-01"}
				if !tc.omit {
					args["description"] = tc.value
				}
				if withConditions {
					args["conditions"] = []WaitCondition{{Type: "run.terminal", RunID: "done"}}
				}
				result, err := (&RuntimeToolExecutor{}).invokeWait(context.Background(), args, nil)
				if err != nil || result.Error != "invalid_wait_arguments" || !strings.Contains(result.Output, "description") {
					t.Fatalf("expected description validation error, got %+v %v", result, err)
				}
			})
		}
		args := map[string]any{"offset": "1S", "description": "  " + strings.Repeat("等", 200) + "  "}
		if withConditions {
			args["conditions"] = []WaitCondition{{Type: "run.terminal", RunID: "done"}}
		}
		parsed, err := parseWaitArguments(args, time.Now())
		if err != nil || parsed.description != strings.Repeat("等", 200) {
			t.Fatalf("valid Unicode description rejected or not trimmed: %+v %v", parsed, err)
		}
	}
}
