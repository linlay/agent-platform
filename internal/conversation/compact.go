package conversation

import (
	"errors"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/compaction"
)

// CompactHistory owns snapshot selection and the atomic history transaction;
// the caller holds the chat maintenance lease for this entire operation.
func CompactHistory(store CompactStore, baseResp api.CompactResponse, window, overhead int, compactID string, generate func(string, int) (string, map[string]any, error)) (api.CompactResponse, error) {
	chatID, requestID, trigger, level := baseResp.ChatID, baseResp.RequestID, baseResp.Trigger, baseResp.Level
	baseResp.CompactID = compactID
	historyTargetTokens := max(1, compaction.Target(window)-overhead)
	keptRunCount := chat.DefaultCompactKeptRunCount
	if level == "l1_tools" {
		target := 0
		if trigger == "auto" {
			target = historyTargetTokens
		}
		return compactHistoryTools(baseResp, store, chatID, requestID, trigger, target, compactID)
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

	summaryInputTokens, summaryOutputTokens := compaction.SummaryBudget(window, overhead+chat.EstimateRawMessageTokens(snapshot.TailMessages))
	if summaryOutputTokens <= 0 {
		baseResp.Status, baseResp.Detail = "failed", "context_window_uncompactable"
		return baseResp, nil
	}
	prompt, promptErr := chat.BuildCompactPromptWithinBudget(snapshot.CoveredMessages, summaryInputTokens)
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
	summaryText, compactionUsage, modelErr := generate(prompt, summaryOutputTokens)
	if modelErr != nil {
		if errors.Is(modelErr, chat.ErrCompactSummaryInputTooLarge) {
			baseResp.Status, baseResp.Detail = "failed", "summary_input_too_large"
			return baseResp, nil
		}
		baseResp.CompactID = compactID
		baseResp.Status = "failed"
		baseResp.Detail = "summary_model_failed"
		baseResp.Retryable = true
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
	if postTokens >= snapshot.PreCompactEstimatedTokens || (historyTargetTokens > 0 && postTokens > historyTargetTokens) {
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

func compactHistoryTools(baseResp api.CompactResponse, store CompactStore, chatID string, requestID string, trigger string, targetTokens int, compactID string) (api.CompactResponse, error) {
	snapshot, err := store.BuildToolCompactSnapshotToTarget(chatID, chat.DefaultToolCompactKeepRecent, targetTokens)
	if err != nil {
		if errors.Is(err, chat.ErrNoCompactableHistory) {
			baseResp.Detail = "no_compactable_tools"
			return baseResp, nil
		}
		return api.CompactResponse{}, err
	}
	baseResp.ToolsKept = snapshot.ToolsKept
	if snapshot.ToolsCleared == 0 {
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
		ToolsKept:                  snapshot.ToolsKept,
		TokensFreed:                snapshot.TokensFreed,
		Detail:                     "completed",
	}, nil
}

type CompactStore interface {
	BuildCompactSnapshot(chatID string, keptRunCount int) (chat.CompactSnapshot, error)
	CommitCompactCheckpoint(chatID string, snapshot chat.CompactSnapshot, checkpoint chat.CompactCheckpointLine) error
	BuildToolCompactSnapshotToTarget(chatID string, keepRecent, targetTokens int) (chat.ToolCompactSnapshot, error)
	CommitToolCompact(chatID string, snapshot chat.ToolCompactSnapshot, line chat.ToolCompactLine) error
}

func compactPercentages(ratio float64) (float64, float64) {
	remaining := max(0.0, min(100.0, ratio*100))
	return remaining, 100 - remaining
}
