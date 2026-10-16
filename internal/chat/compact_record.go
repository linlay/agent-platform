package chat

import (
	"log"
	"strings"

	"agent-platform/internal/stream"
)

// CompactRecordEventKey carries the storage-only facts of a compaction attempt
// on context.compact.complete/failed. It never reaches clients or event lines.
const CompactRecordEventKey = "compactRecord"

func withoutCompactRecord(event stream.EventData) stream.EventData {
	if _, ok := event.Payload[CompactRecordEventKey]; !ok {
		return event
	}
	payload := make(map[string]any, len(event.Payload))
	for key, value := range event.Payload {
		if key != CompactRecordEventKey {
			payload[key] = value
		}
	}
	event.Payload = payload
	return event
}

// appendCompactRecord writes the audit line for one finished compaction. It is
// best effort: losing the audit line must not fail a Run whose context was
// already compacted (or already failed for another reason).
func (w *StepWriter) appendCompactRecord(event stream.EventData, status string, persistErr error) {
	if w == nil || w.store == nil || event.Timestamp <= 0 {
		return
	}
	recordStore, ok := w.store.(CompactRecordStore)
	if !ok {
		return
	}
	line := compactRecordFromEvent(w.chatID, w.runID, event, status)
	if persistErr != nil {
		line.Detail = "compact_persist_failed"
		line.Retryable = true
		line.Error = map[string]any{"code": "compact_persist_failed", "message": persistErr.Error()}
	}
	if err := recordStore.AppendCompactRecord(w.chatID, line); err != nil {
		log.Printf("[chat][%s][run:%s] append compact record %s failed: %v", w.chatID, w.runID, line.CompactID, err)
	}
}

func compactRecordFromEvent(chatID, runID string, event stream.EventData, status string) CompactRecordLine {
	record, _ := event.Value(CompactRecordEventKey).(map[string]any)
	startedAt := int64FromAny(record["startedAt"])
	if startedAt <= 0 || startedAt > event.Timestamp {
		startedAt = event.Timestamp
	}
	compactionUsage, _ := event.Value("compactionUsage").(map[string]any)
	diagnostics, _ := record["diagnostics"].(map[string]any)
	errPayload, _ := record["error"].(map[string]any)
	if eventRunID := strings.TrimSpace(event.String("runId")); eventRunID != "" {
		runID = eventRunID
	}
	return CompactRecordLine{
		Type:                       CompactRecordLineType,
		ChatID:                     chatID,
		RunID:                      runID,
		RequestID:                  event.String("requestId"),
		CompactID:                  event.String("compactId"),
		CycleID:                    event.String("cycleId"),
		Trigger:                    event.String("trigger"),
		Level:                      event.String("level"),
		Scope:                      event.String("scope"),
		Status:                     status,
		Detail:                     event.String("detail"),
		Retryable:                  event.Value("retryable") == true,
		StartedAt:                  startedAt,
		UpdatedAt:                  event.Timestamp,
		DurationMs:                 event.Timestamp - startedAt,
		LiveSeq:                    event.Seq,
		ModelKey:                   strings.TrimSpace(stringFromAny(record["modelKey"])),
		ContextWindow:              toIntFromKeys(record, "contextWindow"),
		SummarySource:              event.String("summarySource"),
		PreCompactEstimatedTokens:  toIntFromKeys(event.Payload, "preCompactEstimatedTokens"),
		PostCompactEstimatedTokens: toIntFromKeys(event.Payload, "postCompactEstimatedTokens"),
		TokensFreed:                toIntFromKeys(event.Payload, "tokensFreed"),
		ToolsCleared:               toIntFromKeys(event.Payload, "toolsCleared"),
		ReasoningCleared:           toIntFromKeys(event.Payload, "reasoningCleared"),
		ToolsKept:                  toIntFromKeys(event.Payload, "toolsKept"),
		CompactionUsage:            cloneStringAnyMap(compactionUsage),
		Diagnostics:                cloneStringAnyMap(diagnostics),
		Error:                      cloneStringAnyMap(errPayload),
	}
}
