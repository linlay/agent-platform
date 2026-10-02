package tools

import (
	"context"
	"math"
	"testing"
	"time"

	"agent-platform/internal/api"
	. "agent-platform/internal/contracts"
)

type sleepTestSink struct{ waits chan ToolWait }

func (s sleepTestSink) EmitToolOutput(context.Context, ToolOutput) error { return nil }
func (s sleepTestSink) EmitToolWait(_ context.Context, w ToolWait) error { s.waits <- w; return nil }

func TestSleepWakeAndCancellation(t *testing.T) {
	for _, mode := range []string{"elapsed", "steer", "queued", "cancel", "interrupt"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			control := NewRunControl(ctx, "same-run")
			defer control.Finish()
			sink := sleepTestSink{make(chan ToolWait, 1)}
			if mode == "queued" {
				control.EnqueueSteer(api.SteerRequest{RunID: "same-run", Message: "new input"})
			}
			done := make(chan ToolExecutionResult, 1)
			duration := 60000
			if mode == "elapsed" {
				duration = 5
			}
			go func() {
				result, err := (&RuntimeToolExecutor{}).invokeSleep(ctx, map[string]any{"durationMs": duration}, &ExecutionContext{RunControl: control, ToolOutputSink: sink})
				if err != nil {
					result.Error = err.Error()
				}
				done <- result
			}()
			select {
			case w := <-sink.waits:
				if w.DeadlineAt-w.StartedAt != int64(duration) {
					t.Fatalf("bad countdown: %#v", w)
				}
			case <-ctx.Done():
				t.Fatal("no countdown")
			}
			if mode == "steer" {
				control.EnqueueSteer(api.SteerRequest{RunID: "same-run", Message: "new input"})
			}
			if mode == "cancel" {
				cancel()
			}
			if mode == "interrupt" {
				control.Interrupt(InterruptInfo{})
			}
			select {
			case result := <-done:
				want := mode
				if mode == "queued" {
					want = "steer"
				}
				if mode == "cancel" || mode == "interrupt" {
					want = "canceled"
				}
				if result.Structured["reason"] != want {
					t.Fatalf("result: %#v", result)
				}
				if want == "steer" {
					steers := control.DrainSteers()
					if len(steers) != 1 || steers[0].Message != "new input" {
						t.Fatalf("input lost: %#v", steers)
					}
				}
			case <-time.After(time.Second):
				t.Fatal("sleep did not wake promptly")
			}
		})
	}
}

func TestSleepInSubTaskIgnoresRootSteer(t *testing.T) {
	control := NewRunControl(context.Background(), "root-run")
	defer control.Finish()
	control.EnqueueSteer(api.SteerRequest{RunID: "root-run", Message: "for the root"})
	execCtx := &ExecutionContext{RunControl: control, Session: QuerySession{SubTaskID: "task-1"}}
	result, err := (&RuntimeToolExecutor{}).invokeSleep(context.Background(), map[string]any{"durationMs": 20}, execCtx)
	if err != nil || result.Structured["reason"] != "elapsed" {
		t.Fatalf("sub-agent sleep woken by root steer: %#v %v", result, err)
	}
	if elapsed, _ := result.Structured["elapsedMs"].(int64); elapsed < 20 {
		t.Fatalf("woke early: %#v", result.Structured)
	}
}

func TestSleepRejectsInvalidDurations(t *testing.T) {
	for _, v := range []any{nil, "1", -1, 0, 1.5, 86400001, math.NaN(), math.Inf(1)} {
		result, err := (&RuntimeToolExecutor{}).invokeSleep(context.Background(), map[string]any{"durationMs": v}, nil)
		if err != nil || result.Error != "invalid_sleep_arguments" {
			t.Fatalf("accepted %#v: %#v %v", v, result, err)
		}
	}
}

func TestSleepRejectsWaitBeyondRunTimeout(t *testing.T) {
	execCtx := &ExecutionContext{StartedAt: time.Now().Add(-50 * time.Second), Budget: Budget{Timeout: 60}}
	result, err := (&RuntimeToolExecutor{}).invokeSleep(context.Background(), map[string]any{"durationMs": 30000}, execCtx)
	if err != nil || result.Error != "sleep_exceeds_run_timeout" {
		t.Fatalf("accepted wait past run timeout: %#v %v", result, err)
	}
	if remaining, _ := result.Structured["remainingMs"].(int64); remaining <= 0 || remaining > 10000 {
		t.Fatalf("bad remaining: %#v", result.Structured)
	}
	execCtx.BudgetPaused = time.Minute
	result, err = (&RuntimeToolExecutor{}).invokeSleep(context.Background(), map[string]any{"durationMs": 5}, execCtx)
	if err != nil || result.Structured["reason"] != "elapsed" {
		t.Fatalf("rejected wait within budget: %#v %v", result, err)
	}
}

func TestSleepPolicyKeepsParentDeadlineAndDoesNotRetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	want, _ := ctx.Deadline()
	calls := 0
	_, _ = (&ToolRouter{}).invokeWithPolicy(ctx, "sleep", &ExecutionContext{Budget: Budget{Tool: RetryPolicy{Timeout: 1, RetryCount: 3}}}, func(ctx context.Context) (ToolExecutionResult, error) {
		calls++
		got, _ := ctx.Deadline()
		if !got.Equal(want) {
			t.Fatalf("ordinary timeout applied: %v", got)
		}
		return ToolExecutionResult{}, context.Canceled
	})
	if calls != 1 {
		t.Fatalf("retried sleep %d times", calls)
	}
}
