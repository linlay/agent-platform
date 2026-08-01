package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
)

func (s *Server) registerRecoveredAwaitingRun(item chat.PendingAwaitingWithChat, step *chat.PersistedAwaitingStep) (contracts.RecoveredAwaitingRun, error) {
	runs, ok := s.deps.Runs.(contracts.RecoveredAwaitingRunService)
	if !ok {
		return contracts.RecoveredAwaitingRun{}, fmt.Errorf("run manager does not support recovered awaiting runs")
	}
	admission, err := s.resolveAwaitingContinuationAdmission(item.ChatID, "")
	if err != nil {
		return contracts.RecoveredAwaitingRun{}, err
	}
	query, err := s.deps.Chats.LoadRunQuery(item.ChatID, item.RunID)
	if err != nil {
		return contracts.RecoveredAwaitingRun{}, err
	}
	var original api.QueryRequest
	if query != nil {
		raw, marshalErr := json.Marshal(query.Query)
		if marshalErr != nil {
			return contracts.RecoveredAwaitingRun{}, marshalErr
		}
		if err := json.Unmarshal(raw, &original); err != nil {
			return contracts.RecoveredAwaitingRun{}, err
		}
	}
	startedAt, err := s.persistedContinuationStartedAt(item.ChatID, item.RunID)
	if err != nil {
		return contracts.RecoveredAwaitingRun{}, err
	}
	owner := contracts.AgentRunOwner(admission.agentKey, "")
	if admission.teamID != "" {
		owner = contracts.TeamRunOwner(admission.teamID, admission.agentKey)
	}
	editingMode := original.EditingMode != nil && *original.EditingMode
	session := contracts.QuerySession{
		RequestID:       original.RequestID,
		RunID:           item.RunID,
		StartedAtMillis: startedAt,
		RunScopeID:      item.ChatID,
		ChatID:          item.ChatID,
		AgentKey:        admission.agentKey,
		TeamID:          admission.teamID,
		RunOwner:        owner,
		AccessLevel:     normalizedAccessLevel(original.AccessLevel),
		EditingMode:     editingMode,
	}
	initialSeq := s.persistedRunLiveSeq(item.ChatID, item.RunID)
	recovered, err := runs.RegisterRecoveredAwaiting(s.backgroundCtx, session, item.AwaitingID, initialSeq)
	if err != nil {
		return contracts.RecoveredAwaitingRun{}, err
	}
	return recovered, nil
}

func (s *Server) superviseRecoveredAwaiting(ctx context.Context, item chat.PendingAwaitingWithChat, step *chat.PersistedAwaitingStep, recovered contracts.RecoveredAwaitingRun) {
	var timeout <-chan time.Time
	var timer *time.Timer
	mode := strings.ToLower(strings.TrimSpace(item.Mode))
	if mode == "question" && step != nil && step.Ask != nil {
		timeoutSeconds := contracts.AnyIntNode(step.Ask.Payload["timeout"])
		if timeoutSeconds > 0 {
			deadline := time.UnixMilli(item.CreatedAt).Add(time.Duration(timeoutSeconds) * time.Second)
			remaining := time.Until(deadline)
			if remaining < 0 {
				remaining = 0
			}
			timer = time.NewTimer(remaining)
			timeout = timer.C
			defer timer.Stop()
		}
	}

	select {
	case <-ctx.Done():
		return
	case <-timeout:
		answer := contracts.AwaitingTimeoutAnswer(mode, int64(contracts.AnyIntNode(step.Ask.Payload["timeout"])), maxInt64((time.Now().UnixMilli()-item.CreatedAt)/1000, 0))
		if _, err := s.finishRecoveredAwaiting(item, step, answer, time.Now().UnixMilli()); err != nil {
			log.Printf("[server][awaiting] finish recovered timeout failed chatId=%s runId=%s awaitingId=%s err=%v", item.ChatID, item.RunID, item.AwaitingID, err)
		}
	case <-recovered.Control.Context().Done():
		if ctx.Err() != nil || s.backgroundCtx.Err() != nil {
			return
		}
		answer := contracts.AwaitingErrorAnswer(mode, "run_interrupted", "Run interrupted while waiting for input")
		errorPayload := contracts.AnyMapNode(answer["error"])
		errorPayload["reason"] = "run_interrupted"
		if _, err := s.finishRecoveredAwaiting(item, step, answer, time.Now().UnixMilli()); err != nil {
			log.Printf("[server][awaiting] finish recovered interrupt failed chatId=%s runId=%s awaitingId=%s err=%v", item.ChatID, item.RunID, item.AwaitingID, err)
		}
	}
}

func (s *Server) finishRecoveredAwaiting(item chat.PendingAwaitingWithChat, step *chat.PersistedAwaitingStep, answer map[string]any, resolvedAt int64) (bool, error) {
	runs, ok := s.deps.Runs.(contracts.RecoveredAwaitingRunService)
	if !ok {
		return false, nil
	}
	claimed, claimedOK := runs.ClaimRecoveredAwaiting(item.RunID, item.AwaitingID)
	if !claimedOK {
		return false, nil
	}
	if err := s.finishRestartTerminalAwaiting(item, step, answer, resolvedAt); err != nil {
		runs.ReleaseRecoveredAwaiting(item.RunID, item.AwaitingID)
		return true, err
	}
	payload := contracts.CloneMap(answer)
	payload["awaitingId"] = item.AwaitingID
	payload["runId"] = item.RunID
	publishRecoveredEvent(claimed.EventBus, "awaiting.answer", resolvedAt, payload)
	for _, toolPayload := range recoveredTerminalToolResults(item, step, answer, resolvedAt) {
		publishRecoveredEvent(claimed.EventBus, "tool.result", resolvedAt, toolPayload)
	}
	publishRecoveredEvent(claimed.EventBus, "run.cancel", resolvedAt, map[string]any{
		"runId":        item.RunID,
		"chatId":       item.ChatID,
		"finishReason": "cancel",
	})
	claimed.EventBus.Freeze()
	s.deps.Runs.Finish(item.RunID)
	s.broadcastDeferredAwaitingAnswer(DeferredAwaiting{
		ChatID: item.ChatID, RunID: item.RunID, AwaitingID: item.AwaitingID, Mode: item.Mode,
	}, payload, resolvedAt)
	s.broadcast("run.finished", runFinishedPushPayload(item.RunID, item.ChatID, resolvedAt))
	return true, nil
}

func recoveredTerminalToolResults(item chat.PendingAwaitingWithChat, step *chat.PersistedAwaitingStep, answer map[string]any, resolvedAt int64) []map[string]any {
	if step == nil {
		return nil
	}
	errorPayload := contracts.AnyMapNode(answer["error"])
	errorCode := strings.TrimSpace(contracts.AnyStringNode(errorPayload["code"]))
	output := strings.TrimSpace(contracts.AnyStringNode(errorPayload["message"]))
	if output == "" {
		output = "tool execution was cancelled before execution"
	}
	duration, _ := awaitingDurationMs(item.CreatedAt, resolvedAt)
	results := make([]map[string]any, 0, len(step.ToolCalls))
	for _, call := range step.ToolCalls {
		toolID := strings.TrimSpace(call.ID)
		if toolID == "" || step.ResultToolIDs[toolID] {
			continue
		}
		payload := map[string]any{
			"toolId":   toolID,
			"toolName": strings.TrimSpace(call.Name),
			"result": map[string]any{
				"error":      errorCode,
				"exitCode":   -1,
				"output":     output,
				"executed":   false,
				"awaitingId": item.AwaitingID,
			},
			"durationMs": duration,
		}
		if taskID := strings.TrimSpace(step.TaskID); taskID != "" {
			payload["taskId"] = taskID
		}
		results = append(results, payload)
	}
	return results
}

func recoveredAnsweredToolResult(step *chat.PersistedAwaitingStep, awaitingID string, answer map[string]any) map[string]any {
	call, ok := deferredAwaitingAnswerToolCall(step, awaitingID)
	if !ok {
		return nil
	}
	payload := map[string]any{
		"toolId":   strings.TrimSpace(call.ID),
		"toolName": strings.TrimSpace(call.Name),
		"result":   contracts.CloneMap(answer),
	}
	if duration := contracts.AnyIntNode(answer["durationMs"]); duration > 0 {
		payload["durationMs"] = duration
	}
	if taskID := strings.TrimSpace(step.TaskID); taskID != "" {
		payload["taskId"] = taskID
	}
	return payload
}

func publishRecoveredEvent(eventBus *stream.RunEventBus, eventType string, timestamp int64, payload map[string]any) stream.EventData {
	if eventBus == nil {
		return stream.EventData{}
	}
	event := stream.EventData{
		Seq:       eventBus.LatestSeq() + 1,
		Type:      eventType,
		Timestamp: timestamp,
		Payload:   payload,
	}
	eventBus.Publish(event)
	return event
}

func (s *Server) completeRecoveredPlanningRun(deferred DeferredAwaiting, recovered *contracts.RecoveredAwaitingRun) error {
	if recovered == nil || recovered.EventBus == nil {
		return fmt.Errorf("recovered planning run is unavailable")
	}
	status, ok := s.deps.Runs.RunStatus(deferred.RunID)
	if !ok {
		return fmt.Errorf("recovered planning run status is unavailable")
	}
	completedAt := time.Now().UnixMilli()
	if completedAt <= status.StartedAt {
		completedAt = status.StartedAt + 1
	}
	summary, err := s.deps.Chats.Summary(deferred.ChatID)
	if err != nil || summary == nil {
		if err == nil {
			err = chat.ErrChatNotFound
		}
		return err
	}
	query, err := s.deps.Chats.LoadRunQuery(deferred.ChatID, deferred.RunID)
	if err != nil {
		return err
	}
	initialMessage := ""
	if query != nil {
		initialMessage = strings.TrimSpace(contracts.AnyStringNode(query.Query["message"]))
	}
	if err := s.deps.Chats.OnRunCompleted(chat.RunCompletion{
		ChatID: deferred.ChatID, RunID: deferred.RunID, AgentKey: summary.AgentKey,
		AgentMode: summary.AgentMode, TeamID: summary.TeamID, InitialMessage: initialMessage,
		FinishReason: "complete", StartedAtMillis: status.StartedAt, UpdatedAtMillis: completedAt,
	}); err != nil {
		return err
	}
	publishRecoveredEvent(recovered.EventBus, "run.complete", completedAt, map[string]any{
		"runId": deferred.RunID, "chatId": deferred.ChatID, "finishReason": "complete",
	})
	if runs, ok := s.deps.Runs.(contracts.RecoveredAwaitingRunService); !ok || !runs.ActivateRecoveredAwaiting(deferred.RunID, deferred.AwaitingID) {
		return fmt.Errorf("complete recovered planning run activation")
	}
	recovered.EventBus.Freeze()
	s.deps.Runs.Finish(deferred.RunID)
	s.broadcast("run.finished", runFinishedPushPayload(deferred.RunID, deferred.ChatID, completedAt))
	return nil
}
