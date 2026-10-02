package tools

import (
	. "agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

func (t *RuntimeToolExecutor) WithWaitConditionProvider(provider WaitConditionProvider) *RuntimeToolExecutor {
	t.waitConditions = provider
	return t
}
func (t *RuntimeToolExecutor) checkWaitCondition(ctx context.Context, c WaitCondition, execCtx *ExecutionContext) (bool, string, error) {
	if strings.HasPrefix(c.Type, "file.") {
		session := t.policySession(execCtx)
		access, err := filetools.BuildAccessPlanFromPolicy(t.cfg.AccessPolicy, session, filetools.ReadAccess, c.FilePath)
		if err != nil {
			return false, "", err
		}
		if access.Blocked || filetools.IsBlockedDeviceFile(access.Path) || (!access.AllowedByWhitelist && !access.AutoApproved) {
			return false, "", fmt.Errorf("wait file condition requires an allowed read path")
		}
		if err := filetools.ValidateScopedRead(session, access.Path, false); err != nil {
			return false, "", err
		}
		info, err := os.Stat(access.Path)
		if os.IsNotExist(err) {
			return false, "missing", nil
		}
		if err != nil {
			return false, "", err
		}
		if c.Type == "file.exists" {
			return true, "exists", nil
		}
		return info.ModTime().UnixMilli() > c.After, "present", nil
	}
	if t.waitConditions == nil {
		return false, "", fmt.Errorf("wait event provider unavailable")
	}
	return t.waitConditions.CheckWaitCondition(ctx, c, execCtx)
}
func (t *RuntimeToolExecutor) invokeWait(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	started := time.Now()
	a, err := parseWaitArguments(args, started)
	if err != nil {
		return ToolExecutionResult{Error: "invalid_wait_arguments", Output: err.Error(), ExitCode: -1}, nil
	}
	if err := ctx.Err(); err != nil {
		return ToolExecutionResult{}, err
	}
	if execCtx != nil && !execCtx.StartedAt.IsZero() && a.deadline.After(execCtx.StartedAt.Add(time.Duration(NormalizeBudget(execCtx.Budget).LifetimeTimeout)*time.Second)) {
		return ToolExecutionResult{Error: "wait_exceeds_run_lifetime", Output: "Wait exceeds the Run lifetime; use automation for longer scheduling.", ExitCode: -1}, nil
	}
	var wake, runDone, skip <-chan struct{}
	var waitControl *RunControl
	waitKey := ""
	if execCtx != nil {
		waitControl = execCtx.RunControl
		waitKey = execCtx.CurrentToolID
		if execCtx.Session.PublicTaskID != "" {
			waitKey = execCtx.Session.PublicTaskID + ":" + waitKey
		}
		if execCtx.Session.WaitControl != nil {
			waitControl = execCtx.Session.WaitControl
		}
	}
	if execCtx != nil && execCtx.RunControl != nil {
		if execCtx.Session.SubTaskID == "" {
			wake = execCtx.RunControl.SteerAvailable()
		}
		runDone = execCtx.RunControl.Context().Done()
		handle := waitControl.RegisterNativeWait(waitKey)
		skip = handle.Done
		defer waitControl.FinishNativeWait(waitKey)
	}
	states := make([]WaitConditionState, len(a.conditions))
	for i, c := range a.conditions {
		states[i] = WaitConditionState{Index: i, Condition: c, Status: "pending"}
	}
	if execCtx != nil && len(execCtx.WaitResumeStates) == len(states) {
		for i, state := range execCtx.WaitResumeStates {
			if state.Condition == states[i].Condition {
				states[i] = state
			}
		}
	}
	checkpoint := &WaitCheckpoint{}
	if execCtx != nil {
		checkpoint = &WaitCheckpoint{StartedAt: execCtx.StartedAt.UnixMilli(), Budget: NormalizeBudget(execCtx.Budget), BudgetPausedMs: execCtx.BudgetPaused.Milliseconds(), WaitCount: execCtx.WaitCount, WaitTotalMs: execCtx.WaitTotal.Milliseconds(), ModelCalls: execCtx.ModelCalls, ToolCalls: execCtx.ToolCalls, ToolRounds: execCtx.ToolRounds, Unrecoverable: execCtx.Session.SubTaskID != "" || execCtx.Session.TeamID != ""}
		if execCtx.RunEnvironment != nil {
			env, _, e := execCtx.RunEnvironment.Snapshot()
			checkpoint.Unrecoverable = checkpoint.Unrecoverable || e != nil || len(env) > 0
		}
	}
	emit := func(update bool) error {
		if execCtx != nil {
			if sink, ok := execCtx.ToolOutputSink.(ToolWaitSink); ok {
				return sink.EmitToolWait(ctx, ToolWait{Checkpoint: checkpoint, StartedAt: started.UnixMilli(), DeadlineAt: a.deadline.UnixMilli(), DurationMs: max(int64(0), a.deadline.UnixMilli()-started.UnixMilli()), Description: a.description, Match: a.match, Conditions: append([]WaitConditionState(nil), states...), Update: update})
			}
		}
		return nil
	}
	reason := ""
	failure := ""
	check := func() bool {
		matched := 0
		for i, c := range a.conditions {
			if !states[i].Satisfied {
				ok, status, e := t.checkWaitCondition(ctx, c, execCtx)
				if e != nil {
					reason = "failed"
					failure = e.Error()
					return true
				}
				states[i].Satisfied = ok
				states[i].Status = status
			}
			if states[i].Satisfied {
				matched++
			}
		}
		if matched > 0 && (a.match == "any" || matched == len(states)) {
			reason = "event"
			return true
		}
		return false
	}
	finished := check()
	if err := emit(false); err != nil {
		return ToolExecutionResult{}, err
	}
	timer := time.NewTimer(max(time.Duration(0), time.Until(a.deadline)))
	defer timer.Stop()
	// Providers inspect authoritative state. Polling here never consumes model
	// calls or Run steps, and avoids subscribe-after-event races.
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for !finished {
		select {
		case <-ctx.Done():
			reason = "canceled"
			finished = true
		case <-runDone:
			reason = "canceled"
			finished = true
		case <-wake:
			reason = "steered"
			finished = true
		case <-skip:
			reason = "skipped"
			finished = true
		case <-timer.C:
			if !check() {
				reason = "elapsed"
				if len(states) > 0 {
					reason = "timeout"
				}
			}
			finished = true
		case <-ticker.C:
			if len(states) > 0 {
				before := fmt.Sprint(states)
				finished = check()
				if before != fmt.Sprint(states) {
					if err := emit(true); err != nil {
						return ToolExecutionResult{}, err
					}
				}
			}
		}
	}
	if ctx.Err() != nil {
		reason = "canceled"
	}
	select {
	case <-runDone:
		reason = "canceled"
	default:
	}
	if execCtx != nil && execCtx.RunControl != nil {
		reason = waitControl.ResolveNativeWait(waitKey, reason)
	}
	ended := time.Now()
	elapsed := ended.Sub(started)
	count := 1
	total := elapsed
	if execCtx != nil {
		execCtx.BudgetPaused += elapsed
		execCtx.WaitCount++
		execCtx.WaitTotal += elapsed
		count = execCtx.WaitCount
		total = execCtx.WaitTotal
	}
	indexes := []int{}
	for i, state := range states {
		if state.Satisfied {
			indexes = append(indexes, i)
		}
	}
	result := structuredResult(map[string]any{"reason": reason, "startedAt": started.UnixMilli(), "deadlineAt": a.deadline.UnixMilli(), "endedAt": ended.UnixMilli(), "elapsedMs": elapsed.Milliseconds(), "resolvedTimezone": a.timezone, "dateOnly": a.dateOnly, "deadlineAlreadyPassed": !a.deadline.After(started), "conditions": states, "matchedConditionIndexes": indexes, "waitStats": map[string]any{"count": count, "totalWaitMs": total.Milliseconds()}})
	if reason == "failed" {
		result.Error = "wait_condition_failed"
		result.Structured["message"] = failure
		encoded := structuredResult(result.Structured)
		result.Output = encoded.Output
		result.ExitCode = -1
	}
	if reason == "canceled" {
		result.Error = "wait_canceled"
		result.ExitCode = -1
	}
	return result, nil
}
