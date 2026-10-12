package query

import (
	"fmt"
	"strings"
	"time"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
)

// abandonPersistedRuns closes work left by a previous process. It never creates
// an executor, restores a session, or submits a saved answer to an Agent.
func (s *Service) abandonPersistedRuns() error {
	if s == nil || s.deps.Chats == nil || s.deferredAwaitings == nil {
		return nil
	}
	items, err := s.deps.Chats.LoadAllPendingAwaitings()
	if err != nil {
		return fmt.Errorf("load pending awaitings: %w", err)
	}
	for _, item := range items {
		if s.hasProcessRun(item.RunID) {
			continue
		}
		if err := s.abandonPersistedAwaiting(item); err != nil {
			return err
		}
	}
	if store, ok := s.deps.Chats.(chat.InterruptedRunStore); ok {
		runs, err := store.LoadUnfinishedRuns()
		if err != nil {
			return fmt.Errorf("load unfinished runs: %w", err)
		}
		for _, run := range runs {
			if s.hasProcessRun(run.RunID) {
				continue
			}
			completedAt := max(time.Now().UnixMilli(), run.StartedAtMillis+1)
			text, err := store.CloseInterruptedRunTools(run.ChatID, run.RunID, completedAt)
			if err != nil {
				return fmt.Errorf("close interrupted run %s: %w", run.RunID, err)
			}
			if err := s.deps.Chats.OnRunCompleted(chat.RunCompletion{ChatID: run.ChatID, RunID: run.RunID, AgentKey: run.AgentKey, AgentMode: run.AgentMode, InitialMessage: run.InitialMessage, AssistantText: text, FinishReason: "cancel", StartedAtMillis: run.StartedAtMillis, UpdatedAtMillis: completedAt}); err != nil {
				return fmt.Errorf("cancel interrupted run %s: %w", run.RunID, err)
			}
			s.finishRunConnectorPins(run.RunID, run.ChatID)
		}
	}
	remaining, err := s.deps.Chats.LoadAllPendingAwaitings()
	if err != nil {
		return err
	}
	s.sweepRunConnectorSnapshots(remaining)
	return nil
}

func (s *Service) hasProcessRun(runID string) bool {
	if s.deps.Runs == nil {
		return false
	}
	status, found := s.deps.Runs.RunStatus(runID)
	return found && status.CompletedAt == 0
}

func (s *Service) abandonPersistedAwaiting(item chat.PendingAwaitingWithChat) error {
	unlock := s.deferredAwaitings.LockResolution(item.ChatID, item.RunID, item.AwaitingID)
	defer unlock()
	if s.hasProcessRun(item.RunID) {
		return nil
	}
	step, err := s.LoadPersistedAwaitingStep(item.ChatID, item.AwaitingID)
	if err != nil {
		return err
	}
	if step == nil || step.Ask == nil {
		return s.deps.Chats.ClearPendingAwaiting(item.ChatID, item.AwaitingID)
	}
	item.RunID = firstNonBlank(item.RunID, step.RunID, step.Ask.RunID)
	item.Mode = firstNonBlank(item.Mode, step.Ask.Mode)
	latest, err := s.deps.Chats.LoadLatestAwaitingSubmit(item.ChatID, item.AwaitingID)
	if err != nil {
		return err
	}
	resolvedAt := max(time.Now().UnixMilli(), item.CreatedAt+1)
	answer := contracts.AwaitingErrorAnswer(item.Mode, "runtime_restarted", "Platform restarted before the Run finished; start a new request")
	contracts.AnyMapNode(answer["error"])["reason"] = "runtime_restarted"
	answerPersisted := latest != nil && restartTerminalAwaitingCode(latest.Answer) != ""
	if answerPersisted {
		answer = contracts.CloneMap(latest.Answer)
		resolvedAt = latest.UpdatedAt
	}
	answer["type"], answer["timestamp"] = "awaiting.answer", resolvedAt
	answer["awaitingId"], answer["runId"] = item.AwaitingID, item.RunID
	if duration, ok := awaitingDurationMs(item.CreatedAt, resolvedAt); ok {
		answer["durationMs"] = duration
	}
	if err := s.persistTerminalAwaiting(item, step, answer, resolvedAt, answerPersisted); err != nil {
		return fmt.Errorf("cancel interrupted awaiting %s: %w", strings.TrimSpace(item.AwaitingID), err)
	}
	s.finishRunConnectorPins(item.RunID, item.ChatID)
	return nil
}
