package chat

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/stream"
)

func TestCompactRecordLinesAuditEveryOutcomeWithoutLeakingToReplay(t *testing.T) {
	store := newCompactTestStore(t)
	chatID := "chat-compact-record"
	ensureCompactTestChat(t, store, chatID)
	appendCompactTestRun(t, store, chatID, "r1", "old user", "old assistant")
	writer := NewStepWriter(store, chatID, "r2", "REACT")
	base := func(compactID string, level string) map[string]any {
		return map[string]any{
			"runId": "r2", "chatId": chatID, "requestId": "req-" + compactID, "compactId": compactID,
			"cycleId": "cycle-1", "trigger": "auto", "level": level, "scope": "run",
		}
	}
	complete := base("compact-1", "summary")
	complete["cycleComplete"] = false
	complete["summarySource"] = "model"
	complete["preCompactEstimatedTokens"] = 1000
	complete["postCompactEstimatedTokens"] = 100
	complete["checkpointMessages"] = []any{
		map[string]any{"role": "user", "content": "checkpoint summary"},
		map[string]any{"role": "user", "content": "current instruction"},
	}
	complete[CompactRecordEventKey] = map[string]any{"startedAt": testEpochMillis(290), "modelKey": "m1", "contextWindow": 262144}
	writer.OnEvent(stream.EventData{Seq: 10, Type: "context.compact.complete", Timestamp: testEpochMillis(300), Payload: complete})

	failed := base("compact-2", "summary")
	failed["cycleComplete"] = true
	failed["detail"] = "summary_model_failed"
	failed["retryable"] = true
	failed[CompactRecordEventKey] = map[string]any{
		"startedAt":     testEpochMillis(400),
		"modelKey":      "m1",
		"contextWindow": 262144,
		"diagnostics":   map[string]any{"summaryTargetTokens": 4096},
		"error":         map[string]any{"code": "provider_timeout", "message": "model stream idle timeout after 60 seconds"},
	}
	writer.OnEvent(stream.EventData{Seq: 11, Type: "context.compact.failed", Timestamp: testEpochMillis(460), Payload: failed})
	if err := writer.Err(); err != nil {
		t.Fatalf("persist compact events: %v", err)
	}

	var records []CompactRecordLine
	var failedEvent map[string]any
	file, err := os.Open(filepath.Join(store.root, chatID+".jsonl"))
	if err != nil {
		t.Fatalf("open jsonl: %v", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		var line map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("decode line: %v", err)
		}
		switch line["_type"] {
		case CompactRecordLineType:
			var record CompactRecordLine
			if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
				t.Fatalf("decode record: %v", err)
			}
			records = append(records, record)
		case "event":
			failedEvent, _ = line["event"].(map[string]any)
		}
	}
	if len(records) != 2 {
		t.Fatalf("compact records = %#v", records)
	}
	ok := records[0]
	if ok.Status != "completed" || ok.CompactID != "compact-1" || ok.RunID != "r2" || ok.StartedAt != testEpochMillis(290) || ok.DurationMs != 10 || ok.ModelKey != "m1" || ok.PostCompactEstimatedTokens != 100 {
		t.Fatalf("completed record = %#v", ok)
	}
	bad := records[1]
	if bad.Status != "failed" || bad.Detail != "summary_model_failed" || !bad.Retryable || bad.DurationMs != 60 || bad.Error["code"] != "provider_timeout" || bad.Diagnostics["summaryTargetTokens"] == nil {
		t.Fatalf("failed record = %#v", bad)
	}
	if failedEvent == nil || failedEvent[CompactRecordEventKey] != nil {
		t.Fatalf("failed event line = %#v", failedEvent)
	}

	if err := completeRunForTest(store, RunCompletion{ChatID: chatID, RunID: "r2", FinishReason: "error", StartedAtMillis: testEpochMillis(250), UpdatedAtMillis: testEpochMillis(470)}); err != nil {
		t.Fatalf("complete run: %v", err)
	}
	if _, err := store.LoadRawMessages(chatID, DefaultHistoryRunWindow); err != nil {
		t.Fatalf("LoadRawMessages: %v", err)
	}
	detail, err := store.LoadChat(chatID)
	if err != nil {
		t.Fatalf("LoadChat: %v", err)
	}
	for _, event := range detail.Events {
		if _, leaked := event.Payload[CompactRecordEventKey]; leaked {
			t.Fatalf("compact record leaked into replay: %#v", event.Payload)
		}
	}
}

func TestHistoryCompactLeavesAuditLinesUnmarked(t *testing.T) {
	store := newCompactTestStore(t)
	chatID := "chat-compact-record-history"
	ensureCompactTestChat(t, store, chatID)
	appendCompactTestRun(t, store, chatID, "r1", "user r1", "assistant r1")
	if err := store.AppendCompactRecord(chatID, CompactRecordLine{CompactID: "compact-old", Level: "l1_tools", Scope: "run", Status: "completed", StartedAt: testEpochMillis(102), UpdatedAt: testEpochMillis(103)}); err != nil {
		t.Fatalf("AppendCompactRecord: %v", err)
	}
	appendCompactTestRun(t, store, chatID, "r2", "user r2", "assistant r2")
	appendCompactTestRun(t, store, chatID, "r3", "user r3", "assistant r3")
	snapshot, err := store.BuildCompactSnapshot(chatID, 1)
	if err != nil {
		t.Fatalf("BuildCompactSnapshot: %v", err)
	}
	if err := store.CommitCompactCheckpoint(chatID, snapshot, CompactCheckpointLine{CompactID: "compact-new", UpdatedAt: testEpochMillis(500), Summary: "summary"}); err != nil {
		t.Fatalf("CommitCompactCheckpoint: %v", err)
	}
	records, _, err := readJSONLineRecords(store.chatJSONLPath(chatID))
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	found, marked := false, 0
	for _, record := range records {
		if record.Value["_type"] == CompactRecordLineType {
			found = true
			if _, ok := record.Value["_compact"]; ok {
				t.Fatalf("audit line was marked: %#v", record.Value)
			}
		} else if lineIsCompacted(record.Value) {
			marked++
		}
	}
	if !found || marked == 0 {
		t.Fatalf("found audit=%t marked=%d", found, marked)
	}
}

func TestHistoryCompactBoundaryIgnoresAuditLinesOfCompactedRuns(t *testing.T) {
	store := newCompactTestStore(t)
	chatID := "chat-compact-record-boundary"
	ensureCompactTestChat(t, store, chatID)
	appendCompactTestRun(t, store, chatID, "r1", "user r1", "assistant r1")
	if err := store.AppendCompactRecord(chatID, CompactRecordLine{RunID: "r1", CompactID: "compact-r1", Level: "l1_tools", Scope: "run", Status: "completed", StartedAt: testEpochMillis(102), UpdatedAt: testEpochMillis(103)}); err != nil {
		t.Fatalf("AppendCompactRecord: %v", err)
	}
	appendCompactTestRun(t, store, chatID, "r2", "user r2", "assistant r2")
	appendCompactTestRun(t, store, chatID, "r3", "user r3", "assistant r3")
	first, err := store.BuildCompactSnapshot(chatID, 2)
	if err != nil {
		t.Fatalf("first snapshot: %v", err)
	}
	if err := store.CommitCompactCheckpoint(chatID, first, CompactCheckpointLine{CompactID: "compact-first", UpdatedAt: testEpochMillis(500), Summary: "summary r1"}); err != nil {
		t.Fatalf("first commit: %v", err)
	}
	second, err := store.BuildCompactSnapshot(chatID, 2)
	if err != nil {
		t.Fatalf("second snapshot: %v", err)
	}
	covered := ""
	for _, message := range second.CoveredMessages {
		covered += stringFromAny(message["content"]) + "\n"
	}
	if !strings.Contains(covered, "user r2") || strings.Contains(covered, "user r3") {
		t.Fatalf("second snapshot covered = %q", covered)
	}
}
