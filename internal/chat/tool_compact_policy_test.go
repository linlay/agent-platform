package chat

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func compactPolicyPair(run, id, tool, text string) []map[string]any {
	return []map[string]any{
		{"role": "assistant", "runId": run, "content": "ordinary assistant text", "tool_calls": []any{map[string]any{"id": id, "type": "function", "function": map[string]any{"name": tool, "arguments": "{\"path\":\"docs/test.md\"}"}}}},
		{"role": "tool", "runId": run, "tool_call_id": id, "content": text},
	}
}

func TestCompactToolProjectionHardWindowCountsUnknownAndPreservesProse(t *testing.T) {
	messages := []map[string]any{{"role": "user", "content": "exact user input\n including spaces  "}}
	for i := 0; i < 7; i++ {
		tool := "file_read"
		if i == 6 {
			tool = "unknown"
		}
		messages = append(messages, compactPolicyPair("r", fmt.Sprint(i), tool, strings.Repeat("body error documentation ", 300))...)
	}
	before, _ := json.Marshal(messages)
	result, cleared, kept := CompactToolMessages(messages, 5, 1, -1, -1)
	if cleared != 2 || kept != 5 {
		t.Fatalf("cleared=%d kept=%d", cleared, kept)
	}
	if !reflect.DeepEqual(result[5:], messages[5:]) || !reflect.DeepEqual(result[0], messages[0]) {
		t.Fatal("recent tools or user changed")
	}
	for i := 1; i < len(result); i += 2 {
		if result[i]["content"] != messages[i]["content"] {
			t.Fatal("assistant prose changed")
		}
	}
	after, _ := json.Marshal(messages)
	if string(before) != string(after) {
		t.Fatal("projection mutated input")
	}
	if !strings.Contains(fmt.Sprint(result[2]["content"]), "status: success") {
		t.Fatal("text containing error was misclassified")
	}
}

func TestCompactToolProjectionProtectsWholeParallelBatch(t *testing.T) {
	messages := compactPolicyPair("r", "first", "bash", strings.Repeat("old ", 2000))
	batch := compactPolicyPair("r", "batch-a", "file_read", strings.Repeat("parallel ", 2000))
	second := compactPolicyPair("r", "batch-b", "file_read", strings.Repeat("parallel ", 2000))
	batch[0]["tool_calls"] = append(batch[0]["tool_calls"].([]any), second[0]["tool_calls"].([]any)...)
	messages = append(messages, batch...)
	messages = append(messages, second[1])
	for i := 0; i < 4; i++ {
		messages = append(messages, compactPolicyPair("r", fmt.Sprint(i), "file_read", "recent")...)
	}
	out, cleared, kept := CompactToolMessages(messages, 5, 1, -1, -1)
	if cleared != 1 || kept != 6 || !reflect.DeepEqual(out[2:], messages[2:]) {
		t.Fatalf("parallel boundary cleared=%d kept=%d", cleared, kept)
	}
}

func TestCompactToolProjectionMatchesRunAndKeepsIncompleteBatch(t *testing.T) {
	messages := compactPolicyPair("old", "same-id", "file_read", strings.Repeat("old ", 2000))
	incomplete := compactPolicyPair("new", "same-id", "file_read", "unreturned")
	messages = append(messages, incomplete[0])
	out, cleared, _ := CompactToolMessages(messages, 0, 0, -1, -1)
	if cleared != 1 || !reflect.DeepEqual(out[2], messages[2]) {
		t.Fatal("reused id stole an older result")
	}
	if !strings.Contains(fmt.Sprint(out[1]["content"]), "[Compacted tool interaction]") {
		t.Fatal("old complete pair not compacted")
	}
}

func TestHistoryL1UsesLatestCheckpointNotCoveredOriginals(t *testing.T) {
	store := newCompactTestStore(t)
	id := "checkpoint-l1-policy"
	ensureCompactTestChat(t, store, id)
	appendCompactTestToolResult(t, store, id, "r0", "obsolete", "bash", strings.Repeat("obsolete ", 4000))
	var messages []map[string]any
	for i := 0; i < 6; i++ {
		messages = append(messages, compactPolicyPair("r1", fmt.Sprint(i), "file_read", strings.Repeat("checkpoint ", 500))...)
	}
	if err := store.AppendRunCompactCheckpoint(id, RunCompactCheckpointLine{Type: RunCompactCheckpointLineType, ChatID: id, RunID: "r1", CompactID: "cp1", UpdatedAt: testEpochMillis(200), Messages: messages}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.BuildToolCompactSnapshotToTarget(id, 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ToolsCleared != 1 || snapshot.ToolsKept != 5 {
		t.Fatalf("snapshot %d/%d", snapshot.ToolsCleared, snapshot.ToolsKept)
	}
	if err := store.CommitToolCompact(id, snapshot, ToolCompactLine{Type: ToolCompactLineType, ChatID: id, CompactID: "l1", UpdatedAt: testEpochMillis(201)}); err != nil {
		t.Fatal(err)
	}
	raw, err := store.LoadRawMessages(id, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 12 || !strings.Contains(fmt.Sprint(raw[1]["content"]), "[Compacted tool interaction]") {
		t.Fatal("checkpoint projection not used")
	}
	if EstimateRawMessageTokens(raw) >= snapshot.PreCompactEstimatedTokens {
		t.Fatal("reported gain did not reduce effective context")
	}
	records, _, err := readJSONLineRecords(store.chatJSONLPath(id))
	if err != nil {
		t.Fatal(err)
	}
	latest := records[len(records)-1].Value
	if latest["previousCompactId"] != "cp1" || int64FromAny(latest["version"]) != 2 || int64FromAny(latest["coveredThroughLine"]) != int64(len(records)-1) {
		t.Fatal("history L1 checkpoint provenance missing")
	}
	if err := store.AppendRunCompactCheckpoint(id, RunCompactCheckpointLine{Type: RunCompactCheckpointLineType, ChatID: id, RunID: "r2", CompactID: "cp2", UpdatedAt: testEpochMillis(202), Messages: raw}); err != nil {
		t.Fatal(err)
	}
	records, _, err = readJSONLineRecords(store.chatJSONLPath(id))
	if err != nil {
		t.Fatal(err)
	}
	if records[len(records)-1].Value["previousCompactId"] != "l1" {
		t.Fatal("new run skipped the intervening history L1")
	}
}

func TestHistorySummaryAfterRunCheckpointKeepsLogicalTail(t *testing.T) {
	store := newCompactTestStore(t)
	id := "checkpoint-summary-policy"
	ensureCompactTestChat(t, store, id)
	for i := 1; i <= 3; i++ {
		appendCompactTestRun(t, store, id, fmt.Sprint(i), "original user "+fmt.Sprint(i), "original answer")
	}
	messages := []map[string]any{{"runId": "1", "role": "user", "content": "logical early anchor"}, {"runId": "2", "role": "user", "content": "logical tail two"}, {"runId": "3", "role": "user", "content": "logical tail three"}}
	if err := store.AppendRunCompactCheckpoint(id, RunCompactCheckpointLine{Type: RunCompactCheckpointLineType, ChatID: id, RunID: "3", CompactID: "cp1", UpdatedAt: testEpochMillis(200), Messages: messages}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.BuildCompactSnapshot(id, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.CoveredMessages) != 1 || len(snapshot.TailMessages) != 2 {
		t.Fatal("source run ownership lost")
	}
	if err := store.CommitCompactCheckpoint(id, snapshot, CompactCheckpointLine{Type: CompactCheckpointLineType, ChatID: id, CompactID: "cp2", UpdatedAt: testEpochMillis(201), Summary: "new summary", SummarySource: "model"}); err != nil {
		t.Fatal(err)
	}
	raw, err := store.LoadRawMessages(id, 20)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(raw)
	if len(raw) != 3 || strings.Contains(string(encoded), "original") || !strings.Contains(string(encoded), "logical tail two") {
		t.Fatal("old context resurrected or tail lost")
	}
	all, err := store.BuildCompactSnapshot(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all.TailMessages) != 0 {
		t.Fatal("explicit zero reset to default")
	}
	detail, err := store.LoadChat(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range detail.Events {
		if strings.HasPrefix(event.Type, "context.compact.") {
			if event.Value("messages") != nil || event.Value("checkpointMessages") != nil {
				t.Fatal("private messages leaked")
			}
		}
	}
}
