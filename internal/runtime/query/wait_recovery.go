package query

import (
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/runtime/adapter"
	sessionbuild "agent-platform/internal/runtime/session"
	runtimetypes "agent-platform/internal/runtime/types"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"
)

func decodeWaitCheckpoint(value any) (contracts.WaitCheckpoint, error) {
	var c contracts.WaitCheckpoint
	raw, err := json.Marshal(value)
	if err == nil {
		err = json.Unmarshal(raw, &c)
	}
	if err == nil && c.StartedAt <= 0 {
		err = fmt.Errorf("missing wait execution checkpoint")
	}
	return c, err
}
func (s *Service) hydrateWait(item chat.PendingAwaitingWithChat, step *chat.PersistedAwaitingStep) error {
	if step == nil || step.Ask == nil {
		return fmt.Errorf("missing wait checkpoint for %s", item.AwaitingID)
	}
	checkpoint, err := decodeWaitCheckpoint(step.Ask.Payload["waitCheckpoint"])
	if err != nil || checkpoint.Unrecoverable || step.TaskID != "" {
		answer := contracts.AwaitingErrorAnswer("wait", "runtime_restarted", "This wait cannot safely recover its execution context")
		_, err = s.FinishTerminalAwaiting(item, answer, time.Now().UnixMilli())
		return err
	}
	recovered, err := s.RegisterRecoveredAwaitingRun(item)
	if err != nil {
		return err
	}
	recovered.Control.TransitionState(contracts.RunLoopStateToolExecuting)
	ctx, cancel := context.WithCancel(s.backgroundCtx)
	deferred := DeferredAwaiting{ChatID: item.ChatID, RunID: item.RunID, AwaitingID: item.AwaitingID, CreatedAt: item.CreatedAt, Mode: "wait", Ask: step.Ask, SupervisorCancel: cancel}
	s.deferredAwaitings.Register(deferred)
	go func() {
		if err := s.resumeWait(ctx, item, step, deferred, recovered, checkpoint); err != nil && ctx.Err() == nil {
			log.Printf("[wait] recovery failed runId=%s toolId=%s err=%v", item.RunID, item.AwaitingID, err)
			answer := contracts.AwaitingErrorAnswer("wait", "wait_recovery_failed", err.Error())
			_, _ = s.FinishTerminalAwaiting(item, answer, time.Now().UnixMilli())
		}
	}()
	return nil
}

type recoveredWaitSink struct {
	recovered     contracts.RecoveredAwaitingRun
	runID, toolID string
	chatID        string
	chats         chat.Store
	step          *chat.PersistedAwaitingStep
}

func (s recoveredWaitSink) EmitToolOutput(context.Context, contracts.ToolOutput) error { return nil }
func (s recoveredWaitSink) EmitToolWait(_ context.Context, w contracts.ToolWait) error {
	now := time.Now().UnixMilli()
	started := int64(contracts.AnyIntNode(s.step.Ask.Payload["startedAt"]))
	payload := map[string]any{"runId": s.runID, "toolId": s.toolID, "toolName": "wait", "startedAt": started, "deadlineAt": w.DeadlineAt, "durationMs": max(int64(0), w.DeadlineAt-started), "description": w.Description, "match": w.Match, "conditions": w.Conditions, "recovered": true}
	persisted := contracts.CloneMap(payload)
	persisted["type"] = "awaiting.ask"
	persisted["mode"] = "wait"
	persisted["awaitingId"] = s.toolID
	persisted["timestamp"] = now
	persisted["waitCheckpoint"] = s.step.Ask.Payload["waitCheckpoint"]
	if err := s.chats.AppendStepLine(s.chatID, chat.StepLine{ChatID: s.chatID, RunID: s.runID, UpdatedAt: now, Seq: s.step.Seq, Stage: s.step.Stage, Type: chat.StepLineTypeReactTool, Awaiting: []map[string]any{persisted}}); err != nil {
		return err
	}
	eventType := "tool.wait"
	if w.Update {
		eventType = "tool.wait.update"
	}
	publishRecoveredEvent(s.recovered.EventBus, eventType, now, payload)
	return nil
}

func (s *Service) resumeWait(ctx context.Context, item chat.PendingAwaitingWithChat, step *chat.PersistedAwaitingStep, deferred DeferredAwaiting, recovered contracts.RecoveredAwaitingRun, checkpoint contracts.WaitCheckpoint) error {
	admission, err := s.resolveAwaitingContinuationAdmission(item.ChatID, "")
	if err != nil {
		return err
	}
	original, err := s.deps.Chats.LoadRunQuery(item.ChatID, item.RunID)
	if err != nil {
		return err
	}
	if original == nil {
		return fmt.Errorf("missing Run query")
	}
	var req runtimetypes.QueryCommand
	raw, _ := json.Marshal(original.Query)
	if err = json.Unmarshal(raw, &req); err != nil {
		return err
	}
	req.RunID = item.RunID
	req.ChatID = item.ChatID
	req.AgentKey = admission.AgentKey
	if err = s.RestoreRunConnectors(item.RunID, &admission.AgentDef); err != nil {
		return err
	}
	session, err := s.deps.Sessions.BuildQuerySession(ctx, req, admission.Summary, admission.AgentDef, sessionbuild.Options{DisableSkillScriptGrants: true, IncludeHistory: true})
	if err != nil {
		return err
	}
	scope, err := s.runControlScopes().Load(item.RunID)
	if err != nil {
		return err
	}
	session.Subject = scope.Subject
	if frozen, err := s.RestoredInteractionPolicy(item.RunID, admission.AgentDef.Mode, original); err != nil {
		return err
	} else if frozen != nil {
		session.InteractionConfig = frozen
	}
	if preparer, ok := s.deps.Agent.(contracts.RunSteerPreparer); ok {
		if err := preparer.BindSteerPreparer(session, recovered.Control); err != nil {
			return err
		}
	}
	args := map[string]any{"base": time.UnixMilli(int64(contracts.AnyIntNode(step.Ask.Payload["deadlineAt"]))).UTC().Format(time.RFC3339Nano), "description": contracts.AnyStringNode(step.Ask.Payload["description"])}
	var states []contracts.WaitConditionState
	raw, _ = json.Marshal(step.Ask.Payload["conditions"])
	if err = json.Unmarshal(raw, &states); err != nil {
		return err
	}
	if len(states) > 0 {
		conditions := make([]contracts.WaitCondition, len(states))
		for i, state := range states {
			conditions[i] = state.Condition
		}
		args["conditions"] = conditions
		args["match"] = contracts.AnyStringNode(step.Ask.Payload["match"])
	}
	started := time.UnixMilli(int64(contracts.AnyIntNode(step.Ask.Payload["startedAt"])))
	execution := &contracts.ExecutionContext{Request: adapter.QueryRequest(req), Session: session, RunControl: recovered.Control, CurrentToolID: item.AwaitingID, CurrentToolName: "wait", WaitResumeStates: states, Budget: checkpoint.Budget, StartedAt: time.UnixMilli(checkpoint.StartedAt), BudgetPaused: time.Duration(checkpoint.BudgetPausedMs) * time.Millisecond, WaitCount: checkpoint.WaitCount, WaitTotal: time.Duration(checkpoint.WaitTotalMs) * time.Millisecond, ModelCalls: checkpoint.ModelCalls, ToolCalls: checkpoint.ToolCalls, ToolRounds: checkpoint.ToolRounds, ToolOutputSink: recoveredWaitSink{recovered: recovered, runID: item.RunID, toolID: item.AwaitingID, chatID: item.ChatID, chats: s.deps.Chats, step: step}}
	var answer map[string]any
	latest, err := s.deps.Chats.LoadLatestAwaitingSubmit(item.ChatID, item.AwaitingID)
	if err != nil {
		return err
	}
	if latest != nil {
		answer = latest.Answer
	} else {
		result, err := s.deps.Tools.Invoke(ctx, "wait", args, execution)
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		answer = result.Structured
		if answer == nil {
			return fmt.Errorf("wait failed: %s", result.Output)
		}
		answer["startedAt"] = started.UnixMilli()
		answer["elapsedMs"] = time.Since(started).Milliseconds()
		answer["mode"] = "wait"
	}
	if recovered.Control.Interrupted() {
		return fmt.Errorf("Run interrupted while recovering wait")
	}
	now := time.Now().UnixMilli()
	if latest != nil {
		if saved, err := decodeWaitCheckpoint(latest.Answer["waitCheckpoint"]); err == nil {
			checkpoint = saved
		}
	} else {
		checkpoint.BudgetPausedMs += now - started.UnixMilli()
		checkpoint.WaitCount++
		checkpoint.WaitTotalMs += now - started.UnixMilli()
	}
	answer["waitStats"] = map[string]any{"count": checkpoint.WaitCount, "totalWaitMs": checkpoint.WaitTotalMs}
	answer["waitCheckpoint"] = checkpoint
	publicAnswer := contracts.CloneMap(answer)
	delete(publicAnswer, "waitCheckpoint")
	runs := s.deps.Runs.(contracts.RecoveredAwaitingRunService)
	claimed, ok := runs.ClaimRecoveredAwaiting(item.RunID, item.AwaitingID)
	if !ok {
		return nil
	}
	defer runs.ReleaseRecoveredAwaiting(item.RunID, item.AwaitingID)
	if err = s.deps.Chats.AppendSubmitLine(item.ChatID, chat.SubmitLine{ChatID: item.ChatID, RunID: item.RunID, UpdatedAt: now, Type: "submit", Answer: withWaitAnswerID(answer, item.AwaitingID)}); err != nil {
		return err
	}
	if err = s.PersistDeferredAwaitingToolAnswer(item.ChatID, item.RunID, item.AwaitingID, publicAnswer, now); err != nil {
		return err
	}
	if err = s.finishWaitRecoveryBatch(item, step, now); err != nil {
		return err
	}
	publishRecoveredEvent(claimed.EventBus, "tool.result", now, map[string]any{"toolId": item.AwaitingID, "toolName": "wait", "result": publicAnswer, "durationMs": now - started.UnixMilli()})
	continued, err := s.startAwaitingContinuationWithAdmission(deferred, queryinput.SubmitRequest{ChatID: item.ChatID, RunID: item.RunID, AwaitingID: item.AwaitingID}, answer, &admission, &claimed)
	if err != nil {
		return err
	}
	if !continued {
		return fmt.Errorf("wait continuation was not started")
	}
	_ = s.deps.Chats.ClearPendingAwaiting(item.ChatID, item.AwaitingID)
	s.deferredAwaitings.Remove(item.AwaitingID)
	return nil
}
func withWaitAnswerID(answer map[string]any, id string) map[string]any {
	out := contracts.CloneMap(answer)
	out["awaitingId"] = id
	out["mode"] = "wait"
	out["type"] = "awaiting.answer"
	out["timestamp"] = answer["endedAt"]
	return out
}

// Wait is a serial barrier. Calls after it in the same model response have not
// started at the checkpoint; finish those slots explicitly so the resumed model
// can decide whether to issue them again, with a complete tool-call group.
func (s *Service) finishWaitRecoveryBatch(item chat.PendingAwaitingWithChat, step *chat.PersistedAwaitingStep, now int64) error {
	after := false
	for _, call := range step.ToolCalls {
		if call.ID == item.AwaitingID {
			after = true
			continue
		}
		if step.ResultToolIDs[call.ID] {
			continue
		}
		payload := map[string]any{"error": "wait_restart_unstarted", "output": "Platform restarted at a wait barrier; this later call was not executed. Issue it again if still needed.", "exitCode": -1, "executed": false}
		if !after {
			payload = map[string]any{"error": "wait_restart_execution_unknown", "output": "No durable result exists for this earlier call; inspect its state before retrying.", "exitCode": -1}
		}
		data, _ := json.Marshal(payload)
		ts := now
		if err := s.deps.Chats.AppendStepLine(item.ChatID, chat.StepLine{ChatID: item.ChatID, RunID: item.RunID, UpdatedAt: now, Seq: step.Seq, Type: chat.StepLineTypeReactTool, Stage: step.Stage, Messages: []chat.StoredMessage{{Role: "tool", Name: call.Name, ToolCallID: call.ID, ToolID: call.ID, Content: []chat.ContentPart{{Type: "text", Text: string(data)}}, Ts: &ts}}}); err != nil {
			return err
		}
	}
	return nil
}
