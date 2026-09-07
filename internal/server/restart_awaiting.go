package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
)

func (s *Server) loadPersistedAwaitingStep(chatID string, awaitingID string) (*chat.PersistedAwaitingStep, error) {
	reader, ok := s.deps.Chats.(chat.AwaitingRecoveryReader)
	if !ok {
		return nil, fmt.Errorf("chat store does not support awaiting recovery")
	}
	return reader.LoadAwaitingStep(chatID, awaitingID)
}

func persistedAwaitingCanContinue(step *chat.PersistedAwaitingStep, publicAwaitingID string) bool {
	if step == nil || step.Ask == nil || len(step.ToolCalls) == 0 {
		return false
	}
	rawAwaitingID := strings.TrimSpace(publicAwaitingID)
	if taskID := strings.TrimSpace(step.TaskID); taskID != "" {
		rawAwaitingID = rawAwaitingIDForTask(taskID, rawAwaitingID)
	}
	for _, call := range step.ToolCalls {
		if strings.TrimSpace(call.ID) == rawAwaitingID {
			return strings.TrimSpace(call.Name) != ""
		}
	}
	return len(step.ToolCalls) == 1 && strings.TrimSpace(step.ToolCalls[0].Name) != ""
}

func restartTerminalAwaitingCode(answer map[string]any) string {
	errorPayload := contracts.AnyMapNode(answer["error"])
	switch code := strings.ToLower(strings.TrimSpace(contracts.AnyStringNode(errorPayload["code"]))); code {
	case "timeout", "runtime_restarted":
		return code
	default:
		return ""
	}
}

// finishTerminalAwaiting is the only entry to persisted terminal reconciliation.
// Live executors and already-claimed recovery shells keep their own write path.
func (s *Server) finishTerminalAwaiting(item chat.PendingAwaitingWithChat, answer map[string]any, resolvedAt int64) (contracts.AwaitingResolutionState, error) {
	state, claimed := contracts.ClaimAwaitingResolution(s.deps.Runs, item.RunID, item.AwaitingID)
	if claimed == nil && state != contracts.AwaitingResolutionUnowned {
		return state, nil
	}
	if s.deferredAwaitings == nil {
		if claimed != nil {
			s.deps.Runs.(contracts.RecoveredAwaitingRunService).ReleaseRecoveredAwaiting(item.RunID, item.AwaitingID)
		}
		return state, fmt.Errorf("awaiting resolution coordinator is required")
	}
	unlock := s.deferredAwaitings.LockResolution(item.ChatID, item.RunID, item.AwaitingID)
	defer unlock()
	if claimed == nil {
		// An earlier reconciler may have finished, or a continuation may now own it.
		state, claimed = contracts.ClaimAwaitingResolution(s.deps.Runs, item.RunID, item.AwaitingID)
		if claimed == nil && state != contracts.AwaitingResolutionUnowned {
			return state, nil
		}
	}
	if claimed != nil {
		defer s.deps.Runs.(contracts.RecoveredAwaitingRunService).ReleaseRecoveredAwaiting(item.RunID, item.AwaitingID)
	}
	// Never reuse a snapshot captured before acquiring ownership and the lock.
	step, err := s.loadPersistedAwaitingStep(item.ChatID, item.AwaitingID)
	if err != nil {
		return state, err
	}
	latest, err := s.deps.Chats.LoadLatestAwaitingSubmit(item.ChatID, item.AwaitingID)
	if err != nil {
		return state, err
	}
	if latest != nil {
		switch code := contracts.AnyStringNode(contracts.AnyMapNode(latest.Answer["error"])["code"]); code {
		case "timeout", "runtime_restarted", "run_interrupted":
			answer = contracts.CloneMap(latest.Answer)
			resolvedAt = latest.UpdatedAt
		default:
			// A submitted answer owns its continuation, even after a partial write.
			return contracts.AwaitingResolutionOwned, nil
		}
	}
	if resolvedAt <= 0 {
		resolvedAt = time.Now().UnixMilli()
	}
	answer = contracts.CloneMap(answer)
	answer["type"] = "awaiting.answer"
	answer["timestamp"] = resolvedAt
	answer["awaitingId"] = item.AwaitingID
	answer["runId"] = item.RunID
	if duration, ok := awaitingDurationMs(item.CreatedAt, resolvedAt); ok {
		answer["durationMs"] = duration
	}
	if err := s.persistTerminalAwaiting(item, step, answer, resolvedAt, latest != nil); err != nil {
		return state, err
	}
	if claimed != nil {
		s.publishRecoveredAwaitingTerminal(item, step, answer, resolvedAt, claimed)
	}
	return contracts.AwaitingResolutionFinished, nil
}

// persistTerminalAwaiting requires recovery ownership or absence of a runtime
// owner, plus the continuation coordinator's per-awaiting resolution lock.
func (s *Server) persistTerminalAwaiting(
	item chat.PendingAwaitingWithChat,
	step *chat.PersistedAwaitingStep,
	answer map[string]any,
	resolvedAt int64,
	answerPersisted bool,
) error {
	if step == nil || step.Ask == nil {
		return fmt.Errorf("terminalize awaiting chatId=%s awaitingId=%s: persisted awaiting step is required", item.ChatID, item.AwaitingID)
	}
	if strings.TrimSpace(step.Ask.AwaitingID) != strings.TrimSpace(item.AwaitingID) {
		return fmt.Errorf("terminalize awaiting chatId=%s awaitingId=%s: persisted awaiting identity does not match", item.ChatID, item.AwaitingID)
	}
	if itemRunID, stepRunID := strings.TrimSpace(item.RunID), strings.TrimSpace(step.RunID); itemRunID != "" && stepRunID != "" && itemRunID != stepRunID {
		return fmt.Errorf("terminalize awaiting chatId=%s awaitingId=%s: persisted runId does not match", item.ChatID, item.AwaitingID)
	}
	if !answerPersisted {
		if err := s.deps.Chats.AppendSubmitLine(item.ChatID, chat.SubmitLine{
			ChatID:    item.ChatID,
			RunID:     item.RunID,
			UpdatedAt: resolvedAt,
			Answer:    answer,
			Type:      "submit",
		}); err != nil {
			return fmt.Errorf("terminalize awaiting chatId=%s awaitingId=%s: append answer: %w", item.ChatID, item.AwaitingID, err)
		}
	}

	if err := s.persistRestartAwaitingToolResults(item, step, answer, resolvedAt); err != nil {
		return err
	}
	if err := s.completeRestartAwaitingRun(item, resolvedAt); err != nil {
		return err
	}
	if err := s.deps.Chats.ClearPendingAwaiting(item.ChatID, item.AwaitingID); err != nil {
		return fmt.Errorf("terminalize awaiting chatId=%s awaitingId=%s: clear pending: %w", item.ChatID, item.AwaitingID, err)
	}
	if s.deferredAwaitings != nil {
		s.deferredAwaitings.Register(DeferredAwaiting{
			ChatID:       item.ChatID,
			AwaitingID:   item.AwaitingID,
			RunID:        item.RunID,
			Mode:         firstNonBlank(item.Mode, step.Ask.Mode, stringValue(answer["mode"])),
			CreatedAt:    item.CreatedAt,
			TerminalCode: restartTerminalAwaitingCode(answer),
		})
	}
	return nil
}

func (s *Server) persistRestartAwaitingToolResults(
	item chat.PendingAwaitingWithChat,
	step *chat.PersistedAwaitingStep,
	answer map[string]any,
	resolvedAt int64,
) error {
	if step == nil || len(step.ToolCalls) == 0 {
		return fmt.Errorf("terminalize awaiting chatId=%s awaitingId=%s: matching tool calls are required", item.ChatID, item.AwaitingID)
	}
	errorPayload := contracts.AnyMapNode(answer["error"])
	errorCode := strings.TrimSpace(contracts.AnyStringNode(errorPayload["code"]))
	if errorCode == "" {
		return fmt.Errorf("terminalize awaiting chatId=%s awaitingId=%s: terminal error code is required", item.ChatID, item.AwaitingID)
	}
	output := strings.TrimSpace(contracts.AnyStringNode(errorPayload["message"]))
	if output == "" {
		output = "tool execution was cancelled before execution"
	}
	duration, _ := awaitingDurationMs(item.CreatedAt, resolvedAt)
	durationPtr := &duration
	messages := make([]chat.StoredMessage, 0, len(step.ToolCalls))
	for _, call := range step.ToolCalls {
		toolID := strings.TrimSpace(call.ID)
		if toolID == "" || step.ResultToolIDs[toolID] {
			continue
		}
		toolName := strings.TrimSpace(call.Name)
		if toolName == "" {
			return fmt.Errorf("terminalize awaiting chatId=%s awaitingId=%s: tool %s has no name", item.ChatID, item.AwaitingID, toolID)
		}
		result, err := json.Marshal(map[string]any{
			"error":      errorCode,
			"exitCode":   -1,
			"output":     output,
			"executed":   false,
			"awaitingId": item.AwaitingID,
		})
		if err != nil {
			return err
		}
		ts := resolvedAt
		messages = append(messages, chat.StoredMessage{
			Role:       "tool",
			Name:       toolName,
			ToolCallID: toolID,
			ToolID:     toolID,
			DurationMs: durationPtr,
			Ts:         &ts,
			Content: []chat.ContentPart{{
				Type: "text",
				Text: string(result),
			}},
		})
	}
	if len(messages) == 0 {
		return nil
	}
	if err := s.deps.Chats.AppendStepLine(item.ChatID, chat.StepLine{
		ChatID:          item.ChatID,
		RunID:           item.RunID,
		UpdatedAt:       resolvedAt,
		TaskID:          step.TaskID,
		TaskStatus:      step.TaskStatus,
		TaskSubAgentKey: step.TaskSubAgentKey,
		TeamID:          step.TeamID,
		Presentation:    step.Presentation,
		Stage:           step.Stage,
		Seq:             step.Seq,
		Messages:        messages,
		Type:            chat.StepLineTypeReactTool,
	}); err != nil {
		return fmt.Errorf("terminalize awaiting chatId=%s awaitingId=%s: append tool results: %w", item.ChatID, item.AwaitingID, err)
	}
	return nil
}

func (s *Server) completeRestartAwaitingRun(item chat.PendingAwaitingWithChat, resolvedAt int64) error {
	runs, err := s.deps.Chats.ListRuns(item.ChatID)
	if err != nil {
		return fmt.Errorf("terminalize awaiting chatId=%s awaitingId=%s: list runs: %w", item.ChatID, item.AwaitingID, err)
	}
	for _, run := range runs {
		if strings.TrimSpace(run.RunID) == strings.TrimSpace(item.RunID) {
			return nil
		}
	}
	reader, ok := s.deps.Chats.(chat.RunStartReader)
	if !ok {
		return fmt.Errorf("terminalize awaiting chatId=%s awaitingId=%s: run start reader is required", item.ChatID, item.AwaitingID)
	}
	startedAt, err := reader.LoadRunStartedAt(item.ChatID, item.RunID)
	if err != nil {
		return fmt.Errorf("terminalize awaiting chatId=%s awaitingId=%s: load run start: %w", item.ChatID, item.AwaitingID, err)
	}
	if resolvedAt <= startedAt {
		resolvedAt = startedAt + 1
	}
	summary, err := s.deps.Chats.Summary(item.ChatID)
	if err != nil || summary == nil {
		if err == nil {
			err = chat.ErrChatNotFound
		}
		return fmt.Errorf("terminalize awaiting chatId=%s awaitingId=%s: load summary: %w", item.ChatID, item.AwaitingID, err)
	}
	query, err := s.deps.Chats.LoadRunQuery(item.ChatID, item.RunID)
	if err != nil {
		return fmt.Errorf("terminalize awaiting chatId=%s awaitingId=%s: load query: %w", item.ChatID, item.AwaitingID, err)
	}
	initialMessage := ""
	if query != nil {
		initialMessage = strings.TrimSpace(contracts.AnyStringNode(query.Query["message"]))
	}
	if err := s.deps.Chats.OnRunCompleted(chat.RunCompletion{
		ChatID:          item.ChatID,
		RunID:           item.RunID,
		AgentKey:        summary.AgentKey,
		AgentMode:       summary.AgentMode,
		TeamID:          summary.TeamID,
		InitialMessage:  initialMessage,
		FinishReason:    "cancel",
		StartedAtMillis: startedAt,
		UpdatedAtMillis: resolvedAt,
	}); err != nil {
		return fmt.Errorf("terminalize awaiting chatId=%s awaitingId=%s: complete run: %w", item.ChatID, item.AwaitingID, err)
	}
	return nil
}
