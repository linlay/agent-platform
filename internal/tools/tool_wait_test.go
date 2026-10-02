package tools

import (
	"agent-platform/internal/api"
	. "agent-platform/internal/contracts"
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type waitTestSink struct{ waits chan ToolWait }

func (s waitTestSink) EmitToolOutput(context.Context, ToolOutput) error { return nil }
func (s waitTestSink) EmitToolWait(_ context.Context, w ToolWait) error { s.waits <- w; return nil }
func TestWaitOffsetPriorityAndValidation(t *testing.T) {
	now := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	args, err := parseWaitArguments(map[string]any{"offset": "+1H30m", "base": "invalid ignored date"}, now)
	if err != nil || args.deadline.Sub(now) != 90*time.Minute {
		t.Fatalf("%+v %v", args, err)
	}
	for _, offset := range []any{"+5M", "+1w", "-5m", "0S", "1.5S", "25H", "", nil} {
		if _, err := parseWaitArguments(map[string]any{"offset": offset, "base": "2026-10-02T11:00:00Z"}, now); err == nil {
			t.Fatalf("accepted %v", offset)
		}
	}
	if _, err := parseWaitArguments(map[string]any{}, now); err == nil {
		t.Fatal("time upper bound required")
	}
}
func TestWaitWakeAndBudget(t *testing.T) {
	for _, reason := range []string{"steered", "skipped", "canceled"} {
		t.Run(reason, func(t *testing.T) {
			control := NewRunControl(context.Background(), "root")
			defer control.Finish()
			sink := waitTestSink{make(chan ToolWait, 2)}
			exec := &ExecutionContext{RunControl: control, CurrentToolID: "call", StartedAt: time.Now(), Budget: Budget{Timeout: 1}, ToolOutputSink: sink}
			done := make(chan ToolExecutionResult, 1)
			go func() {
				result, _ := (&RuntimeToolExecutor{}).invokeWait(context.Background(), map[string]any{"offset": "+1H"}, exec)
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
			case "skipped":
				if control.SkipNativeWait("call") != "accepted" {
					t.Fatal("skip failed")
				}
			case "canceled":
				control.Interrupt(InterruptInfo{})
			}
			select {
			case result := <-done:
				if result.Structured["reason"] != reason {
					t.Fatalf("%+v", result)
				}
				if exec.WaitCount != 1 || exec.BudgetPaused <= 0 {
					t.Fatal("budget not paused")
				}
			case <-time.After(time.Second):
				t.Fatal("wait did not wake")
			}
			if control.SkipNativeWait("call") != "already_resolved" {
				t.Fatal("late skip not idempotent")
			}
			if reason == "steered" && len(control.DrainSteers()) != 1 {
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
		result, err := executor.invokeWait(context.Background(), map[string]any{"base": time.Now().Add(20 * time.Millisecond).UTC().Format(time.RFC3339Nano), "conditions": conditions, "match": match}, nil)
		want := "event"
		if match == "all" {
			want = "timeout"
		}
		if err != nil || result.Structured["reason"] != want {
			t.Fatalf("%s: %+v %v", match, result, err)
		}
	}
	provider.count.Store(1)
	result, _ := executor.invokeWait(context.Background(), map[string]any{"offset": "1H", "conditions": conditions, "match": "all"}, nil)
	if result.Structured["reason"] != "event" {
		t.Fatal(result)
	}
}
func TestWaitPastDateAndLifetime(t *testing.T) {
	result, _ := (&RuntimeToolExecutor{}).invokeWait(context.Background(), map[string]any{"base": "2020-01-01"}, nil)
	if result.Structured["reason"] != "elapsed" || result.Structured["dateOnly"] != true || result.Structured["deadlineAlreadyPassed"] != true {
		t.Fatal(result)
	}
	result, _ = (&RuntimeToolExecutor{}).invokeWait(context.Background(), map[string]any{"offset": "1H"}, &ExecutionContext{StartedAt: time.Now(), Budget: Budget{LifetimeTimeout: 10}})
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

func TestChildWaitUsesPublicIdentityWithoutConsumingRootInput(t *testing.T) {
	root := NewRunControl(context.Background(), "root")
	defer root.Finish()
	child := NewRunControl(context.Background(), "child")
	defer child.Finish()
	root.EnqueueSteer(api.SteerRequest{Message: "root only"})
	sink := waitTestSink{make(chan ToolWait, 2)}
	done := make(chan ToolExecutionResult, 1)
	go func() {
		result, _ := (&RuntimeToolExecutor{}).invokeWait(context.Background(), map[string]any{"offset": "1H"}, &ExecutionContext{RunControl: child, CurrentToolID: "call", Session: QuerySession{SubTaskID: "sub_1", PublicTaskID: "root_t_1", WaitControl: root}, ToolOutputSink: sink})
		done <- result
	}()
	select {
	case <-sink.waits:
	case <-time.After(time.Second):
		t.Fatal("no child wait")
	}
	if root.SkipNativeWait("root_t_1:call") != "accepted" {
		t.Fatal("public tool ID cannot skip child")
	}
	select {
	case result := <-done:
		if result.Structured["reason"] != "skipped" {
			t.Fatal(result)
		}
	case <-time.After(time.Second):
		t.Fatal("child skip did not wake")
	}
	if len(root.DrainSteers()) != 1 {
		t.Fatal("root input lost")
	}
}
