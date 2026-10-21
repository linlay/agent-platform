package conversation

import (
	"errors"
	"log"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/apperrors"
	"agent-platform/internal/chat"
	"agent-platform/internal/compaction"
)

// CompactHistory owns snapshot selection and the atomic history transaction;
// the caller holds the chat maintenance lease for this entire operation.
// Every attempt, whatever its outcome, leaves a _type:"compact" audit line.
func CompactHistory(store CompactStore, baseResp api.CompactResponse, modelKey string, window, overhead int, compactID string, generate func(string, int) (string, map[string]any, error), options ...chat.L1Options) (api.CompactResponse, error) {
	startedAt := time.Now().UnixMilli()
	trace := &historyCompactTrace{}
	trace.set("overheadTokens", overhead)
	resp, err := compactHistory(store, baseResp, window, overhead, compactID, generate, trace, options...)
	appendHistoryCompactRecord(store, baseResp, resp, compactID, modelKey, window, startedAt, err, trace)
	return resp, err
}

// historyCompactTrace collects what a history compaction learned before it
// finished, so the audit line keeps estimates and budgets on failure too.
type historyCompactTrace struct {
	preTokens   int
	diagnostics map[string]any
	summaryErr  error
}

func (t *historyCompactTrace) set(key string, value any) {
	if t.diagnostics == nil {
		t.diagnostics = map[string]any{}
	}
	t.diagnostics[key] = value
}

func appendHistoryCompactRecord(store CompactStore, baseResp, resp api.CompactResponse, compactID, modelKey string, window int, startedAt int64, err error, trace *historyCompactTrace) {
	recordStore, ok := store.(chat.CompactRecordStore)
	if !ok {
		return
	}
	completedAt := time.Now().UnixMilli()
	status := "skipped"
	switch {
	case err != nil:
		status = "failed"
	case resp.Status == "completed" || resp.Status == "failed":
		status = resp.Status
	}
	line := chat.CompactRecordLine{
		RequestID:                  baseResp.RequestID,
		CompactID:                  compactID,
		Trigger:                    baseResp.Trigger,
		Level:                      baseResp.Level,
		Scope:                      "history",
		Status:                     status,
		Detail:                     resp.Detail,
		Retryable:                  resp.Retryable,
		StartedAt:                  startedAt,
		UpdatedAt:                  completedAt,
		DurationMs:                 completedAt - startedAt,
		ModelKey:                   modelKey,
		ContextWindow:              window,
		SummarySource:              resp.SummarySource,
		PreCompactEstimatedTokens:  resp.PreCompactEstimatedTokens,
		PostCompactEstimatedTokens: resp.PostCompactEstimatedTokens,
		TokensFreed:                resp.TokensFreed,
		ToolsCleared:               resp.ToolsCleared,
		ReasoningCleared:           resp.ReasoningCleared,
		ToolsKept:                  resp.ToolsKept,
		CompactionUsage:            resp.CompactionUsage,
		Diagnostics:                trace.diagnostics,
	}
	if line.Level == "" {
		line.Level = "summary"
	}
	if line.PreCompactEstimatedTokens == 0 {
		line.PreCompactEstimatedTokens = trace.preTokens
	}
	if line.PostCompactEstimatedTokens == 0 {
		line.PostCompactEstimatedTokens, _ = trace.diagnostics["postCompactEstimatedTokens"].(int)
	}
	switch {
	case trace.summaryErr != nil:
		line.Error = apperrors.FromError(trace.summaryErr, apperrors.CodeContextCompactFailed)
	case err != nil:
		line.Error = apperrors.FromError(err, apperrors.CodeRunError)
	}
	if appendErr := recordStore.AppendCompactRecord(baseResp.ChatID, line); appendErr != nil {
		log.Printf("[compact][%s] append compact record %s failed: %v", baseResp.ChatID, compactID, appendErr)
	}
}

func compactHistory(store CompactStore, baseResp api.CompactResponse, window, overhead int, compactID string, generate func(string, int) (string, map[string]any, error), trace *historyCompactTrace, options ...chat.L1Options) (api.CompactResponse, error) {
	chatID, requestID, trigger, level := baseResp.ChatID, baseResp.RequestID, baseResp.Trigger, baseResp.Level
	baseResp.CompactID = compactID
	historyTargetTokens := max(1, compaction.Target(window)-overhead)
	keptRunCount := chat.DefaultCompactKeptRunCount
	if level == "l1_tools" {
		option := chat.L1Options{}
		if len(options) > 0 {
			option = options[0]
		}
		option.KeepRecent = compaction.KeepRecentRounds(window, option.KeepRecent)
		return compactHistoryL1(baseResp, store, chatID, requestID, trigger, compactID, option, trace)
	}
	snapshot, err := store.BuildCompactSnapshot(chatID, keptRunCount)
	if err != nil {
		if errors.Is(err, chat.ErrNoCompactableHistory) {
			baseResp.Detail = "no_compactable_history"
			return baseResp, nil
		}
		return api.CompactResponse{}, err
	}
	for historyTargetTokens > 0 && chat.EstimateRawMessageTokens(snapshot.TailMessages)+min(4096, window/10)+64 > historyTargetTokens && keptRunCount > 0 {
		keptRunCount--
		snapshot, err = store.BuildCompactSnapshot(chatID, keptRunCount)
		if err != nil {
			if errors.Is(err, chat.ErrNoCompactableHistory) {
				baseResp.Detail = "no_compactable_history"
				return baseResp, nil
			}
			return api.CompactResponse{}, err
		}
	}

	trace.preTokens = snapshot.PreCompactEstimatedTokens
	trace.set("keptRunCount", keptRunCount)
	trace.set("candidateMessages", len(snapshot.CoveredMessages))
	trace.set("retainedMessages", len(snapshot.TailMessages))
	summaryInputTokens, summaryTargetTokens := compaction.SummaryBudget(window, overhead+chat.EstimateRawMessageTokens(snapshot.TailMessages))
	trace.set("summaryMaxInputTokens", summaryInputTokens)
	trace.set("summaryTargetTokens", summaryTargetTokens)
	if summaryTargetTokens <= 0 {
		baseResp.Status, baseResp.Detail = "failed", "context_window_uncompactable"
		return baseResp, nil
	}
	prompt, promptErr := chat.BuildCompactPromptWithinBudget(snapshot.CoveredMessages, summaryInputTokens, summaryTargetTokens)
	if errors.Is(promptErr, chat.ErrCompactSummaryInputTooLarge) {
		baseResp.CompactID = compactID
		baseResp.Status = "failed"
		baseResp.Detail = "summary_input_too_large"
		return baseResp, nil
	}
	if promptErr != nil || strings.TrimSpace(prompt) == "" || generate == nil {
		baseResp.CompactID = compactID
		baseResp.Status = "failed"
		baseResp.Detail = "summary_model_failed"
		baseResp.Retryable = true
		return baseResp, nil
	}
	trace.set("summaryPromptChars", len(prompt))
	trace.set("summaryPromptEstimatedTokens", chat.EstimateTextTokens(prompt))
	summaryStartedAt := time.Now()
	summaryText, compactionUsage, modelErr := generate(prompt, summaryTargetTokens)
	trace.set("summaryDurationMs", time.Since(summaryStartedAt).Milliseconds())
	if modelErr != nil {
		trace.summaryErr = modelErr
		if errors.Is(modelErr, chat.ErrCompactSummaryInputTooLarge) {
			baseResp.Status, baseResp.Detail = "failed", "summary_input_too_large"
			return baseResp, nil
		}
		baseResp.CompactID = compactID
		baseResp.Status = "failed"
		baseResp.Detail = "summary_model_failed"
		baseResp.Retryable = apperrors.RetryableOr(modelErr, true)
		return baseResp, nil
	}
	summaryText = strings.TrimSpace(summaryText)
	if summaryText == "" || summaryText == "Model returned no assistant content." {
		baseResp.CompactID = compactID
		baseResp.Status = "failed"
		baseResp.Detail = "summary_empty"
		baseResp.Retryable = true
		return baseResp, nil
	}
	summarySource := "model"

	postTokens := chat.EstimateCompactPostTokens(summaryText, snapshot.TailMessages)
	if postTokens >= snapshot.PreCompactEstimatedTokens || (window > 0 && postTokens+overhead+64 >= window) {
		trace.set("postCompactEstimatedTokens", postTokens)
		baseResp.CompactID = compactID
		baseResp.Status = "failed"
		baseResp.Detail = "context_window_uncompactable"
		return baseResp, nil
	}
	ratio := 0.0
	if snapshot.PreCompactEstimatedTokens > 0 {
		ratio = float64(postTokens) / float64(snapshot.PreCompactEstimatedTokens)
	}
	remainingRatio, releasedRatio := compactPercentages(ratio)
	checkpoint := chat.CompactCheckpointLine{
		Type:                       chat.CompactCheckpointLineType,
		ChatID:                     chatID,
		CompactID:                  compactID,
		UpdatedAt:                  time.Now().UnixMilli(),
		Trigger:                    trigger,
		Summary:                    summaryText,
		SummarySource:              summarySource,
		PreCompactEstimatedTokens:  snapshot.PreCompactEstimatedTokens,
		PostCompactEstimatedTokens: postTokens,
		CompressionRatio:           ratio,
		RemainingRatio:             remainingRatio,
		ReleasedRatio:              releasedRatio,
		TokensFreed:                max(snapshot.PreCompactEstimatedTokens-postTokens, 0),
		CompactionUsage:            compactionUsage,
	}
	if err := store.CommitCompactCheckpoint(chatID, snapshot, checkpoint); err != nil {
		if errors.Is(err, chat.ErrCompactHistoryChanged) {
			baseResp.CompactID = compactID
			baseResp.Detail = "history_changed"
			baseResp.Retryable = true
			return baseResp, nil
		}
		if errors.Is(err, chat.ErrNoCompactableHistory) {
			baseResp.CompactID = compactID
			baseResp.Detail = "no_compactable_history"
			return baseResp, nil
		}
		baseResp.CompactID = compactID
		baseResp.Status = "failed"
		baseResp.Detail = "compact_persist_failed"
		baseResp.Retryable = true
		return baseResp, nil
	}

	return api.CompactResponse{
		Accepted:                   true,
		Status:                     "completed",
		RequestID:                  requestID,
		ChatID:                     chatID,
		CompactID:                  compactID,
		Trigger:                    trigger,
		Scope:                      "history",
		Level:                      level,
		SummarySource:              summarySource,
		PreCompactEstimatedTokens:  snapshot.PreCompactEstimatedTokens,
		PostCompactEstimatedTokens: postTokens,
		CompressionRatio:           ratio,
		RemainingRatio:             remainingRatio,
		ReleasedRatio:              releasedRatio,
		TokensFreed:                max(snapshot.PreCompactEstimatedTokens-postTokens, 0),
		CompactionUsage:            compactionUsage,
		Detail:                     "completed",
	}, nil
}

func compactHistoryL1(baseResp api.CompactResponse, store CompactStore, chatID string, requestID string, trigger string, compactID string, option chat.L1Options, trace *historyCompactTrace) (api.CompactResponse, error) {
	snapshot, err := store.BuildL1CompactSnapshot(chatID, option)
	if err != nil {
		if errors.Is(err, chat.ErrNoCompactableHistory) {
			baseResp.Detail = "no_compactable_tools"
			return baseResp, nil
		}
		return api.CompactResponse{}, err
	}
	trace.preTokens = snapshot.PreCompactEstimatedTokens
	trace.set("l1KeepRecent", option.KeepRecent)
	baseResp.ToolsKept = snapshot.ToolsKept
	if snapshot.ToolsCleared == 0 && snapshot.ReasoningCleared == 0 {
		baseResp.Detail = "no_compactable_tools"
		return baseResp, nil
	}

	line := chat.ToolCompactLine{
		Type:                       chat.ToolCompactLineType,
		ChatID:                     chatID,
		CompactID:                  compactID,
		UpdatedAt:                  time.Now().UnixMilli(),
		Trigger:                    trigger,
		Level:                      "l1_tools",
		ToolsCleared:               snapshot.ToolsCleared,
		ReasoningCleared:           snapshot.ReasoningCleared,
		ToolsKept:                  snapshot.ToolsKept,
		TokensFreed:                snapshot.TokensFreed,
		PreCompactEstimatedTokens:  snapshot.PreCompactEstimatedTokens,
		PostCompactEstimatedTokens: snapshot.PostCompactEstimatedTokens,
		CompressionRatio:           snapshot.CompressionRatio,
		RemainingRatio:             snapshot.CompressionRatio * 100,
		ReleasedRatio:              100 - snapshot.CompressionRatio*100,
	}
	if err := store.CommitToolCompact(chatID, snapshot, line); err != nil {
		if errors.Is(err, chat.ErrCompactHistoryChanged) {
			baseResp.CompactID = compactID
			baseResp.Detail = "history_changed"
			baseResp.Retryable = true
			return baseResp, nil
		}
		if errors.Is(err, chat.ErrNoCompactableHistory) {
			baseResp.CompactID = compactID
			baseResp.Detail = "no_compactable_tools"
			return baseResp, nil
		}
		baseResp.CompactID = compactID
		baseResp.Status = "failed"
		baseResp.Detail = "compact_persist_failed"
		baseResp.Retryable = true
		return baseResp, nil
	}

	return api.CompactResponse{
		Accepted:                   true,
		Status:                     "completed",
		RequestID:                  requestID,
		ChatID:                     chatID,
		CompactID:                  compactID,
		Trigger:                    trigger,
		Scope:                      "history",
		Level:                      "l1_tools",
		PreCompactEstimatedTokens:  snapshot.PreCompactEstimatedTokens,
		PostCompactEstimatedTokens: snapshot.PostCompactEstimatedTokens,
		CompressionRatio:           snapshot.CompressionRatio,
		RemainingRatio:             snapshot.CompressionRatio * 100,
		ReleasedRatio:              100 - snapshot.CompressionRatio*100,
		ToolsCleared:               snapshot.ToolsCleared,
		ReasoningCleared:           snapshot.ReasoningCleared,
		ToolsKept:                  snapshot.ToolsKept,
		TokensFreed:                snapshot.TokensFreed,
		Detail:                     "completed",
	}, nil
}

type CompactStore interface {
	BuildCompactSnapshot(chatID string, keptRunCount int) (chat.CompactSnapshot, error)
	CommitCompactCheckpoint(chatID string, snapshot chat.CompactSnapshot, checkpoint chat.CompactCheckpointLine) error
	BuildL1CompactSnapshot(chatID string, option chat.L1Options) (chat.ToolCompactSnapshot, error)
	CommitToolCompact(chatID string, snapshot chat.ToolCompactSnapshot, line chat.ToolCompactLine) error
}

func compactPercentages(ratio float64) (float64, float64) {
	remaining := max(0.0, min(100.0, ratio*100))
	return remaining, 100 - remaining
}
