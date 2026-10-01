package tools

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"time"

	. "agent-platform/internal/contracts"
)

func (t *RuntimeToolExecutor) invokeSleep(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	// Validate here as well as in the model schema: direct tool callers bypass it.
	var ms float64
	switch v := args["duration_ms"].(type) {
	case float64:
		ms = v
	case int:
		ms = float64(v)
	case int64:
		ms = float64(v)
	case json.Number:
		ms, _ = v.Float64()
	}
	if len(args) != 1 || math.IsNaN(ms) || math.IsInf(ms, 0) || ms < 1 || ms > 86400000 || math.Trunc(ms) != ms {
		return ToolExecutionResult{Error: "invalid_sleep_arguments", Output: "duration_ms must be an integer between 1 and 86400000; no other arguments are accepted", ExitCode: -1}, nil
	}
	if err := ctx.Err(); err != nil {
		return ToolExecutionResult{}, err
	}
	// Sleep counts toward the Run timeout, which is only checked before the next
	// model call; refuse a wait that would keep the Run alive past its budget.
	if execCtx != nil && !execCtx.StartedAt.IsZero() {
		remaining := NormalizeBudget(execCtx.Budget).RunTimeout() - (time.Since(execCtx.StartedAt) - execCtx.BudgetPaused)
		if remaining < 0 {
			remaining = 0
		}
		if time.Duration(ms)*time.Millisecond > remaining {
			result := structuredResult(map[string]any{"durationMs": int64(ms), "remainingMs": remaining.Milliseconds()})
			result.Error, result.ExitCode = "sleep_exceeds_run_timeout", -1
			return result, nil
		}
	}
	var wake, runDone <-chan struct{}
	if execCtx != nil && execCtx.RunControl != nil {
		// A sub-agent may share the root RunControl; steer addresses the root
		// Run, so only the root loop's sleep is woken by it.
		if strings.TrimSpace(execCtx.Session.SubTaskID) == "" {
			wake = execCtx.RunControl.SteerAvailable()
		}
		runDone = execCtx.RunControl.Context().Done()
	}
	started := time.Now()
	duration := time.Duration(ms) * time.Millisecond
	deadline := started.Add(duration)
	if execCtx != nil {
		if sink, ok := execCtx.ToolOutputSink.(ToolWaitSink); ok {
			if err := sink.EmitToolWait(ctx, ToolWait{StartedAt: started.UnixMilli(), DeadlineAt: deadline.UnixMilli(), DurationMs: int64(ms)}); err != nil {
				return ToolExecutionResult{}, err
			}
		}
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	reason := "elapsed"
	select {
	case <-ctx.Done():
		reason = "canceled"
	case <-runDone:
		reason = "canceled"
	case <-wake:
		reason = "steer"
	case <-timer.C:
	}
	// Cancellation wins if it races timer expiry or a queued steer.
	if ctx.Err() != nil {
		reason = "canceled"
	}
	select {
	case <-runDone:
		reason = "canceled"
	default:
	}
	result := structuredResult(map[string]any{
		"reason": reason, "durationMs": int64(ms), "elapsedMs": time.Since(started).Milliseconds(),
		"startedAt": started.UnixMilli(), "deadlineAt": deadline.UnixMilli(), "endedAt": time.Now().UnixMilli(),
	})
	if reason == "canceled" {
		result.Error, result.ExitCode = "sleep_canceled", -1
	}
	return result, nil
}
