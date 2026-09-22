package chat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestCompactCategoryProjectionPreservesOriginal(t *testing.T) {
	line := map[string]any{"_type": StepLineTypeReact, "runId": "r", "seq": 1, "messages": []any{
		map[string]any{"role": "assistant", "content": "answer", "reasoning_content": "thinking", "tool_calls": []any{map[string]any{"id": "t", "function": map[string]any{"name": "bash", "arguments": "{}"}}}},
		map[string]any{"role": "tool", "tool_call_id": "t", "content": "result"},
	}}
	before, _ := json.Marshal(line)
	for _, tc := range []struct {
		keep         []string
		count        int
		reason, tool bool
	}{
		{[]string{"content"}, 1, false, false}, {[]string{"content", "tool"}, 2, false, true}, {[]string{"reasoning"}, 1, true, false}, {nil, 0, false, false},
	} {
		value := cloneJSONLineMap(line)
		keep := map[string]bool{}
		for _, key := range tc.keep {
			keep[key] = true
		}
		value["_compact"] = compactMarker("L1", "id", keep)
		got := rawMessagesFromJSONLLines([]map[string]any{value})
		if len(got) != tc.count {
			t.Fatalf("keep=%v got=%v", tc.keep, got)
		}
		if len(got) > 0 && ((got[0]["reasoning_content"] != nil) != tc.reason || (len(anyMessageSlice(got[0]["tool_calls"])) > 0) != tc.tool) {
			t.Fatalf("wrong projection %v", got)
		}
	}
	after, _ := json.Marshal(line)
	if !bytes.Equal(before, after) {
		t.Fatal("projection mutated source")
	}
	legacy := cloneJSONLineMap(line)
	legacy["_compact"] = "old"
	if len(rawMessagesFromJSONLLines([]map[string]any{legacy})) != 0 {
		t.Fatal("legacy marker replayed")
	}
}

func TestL1ProtectsCompletedModelRoundsIncludingTextOnlyAndParallel(t *testing.T) {
	messages := []map[string]any{{"role": "assistant", "content": "old answer", "reasoning_content": "old reasoning"}}
	// Five text-only model calls count as five protected rounds.
	for i := 0; i < 5; i++ {
		messages = append(messages, map[string]any{"role": "assistant", "content": fmt.Sprint(i), "reasoning_content": "recent"})
	}
	before, _ := json.Marshal(messages)
	got := ProjectL1(messages, 5, -1, -1, false)
	if got.ReasoningCleared != 1 || got.Messages[0]["content"] != "old answer" || got.Messages[0]["reasoning_content"] != nil {
		t.Fatal("wrong old-round projection")
	}
	if !reflect.DeepEqual(got.Messages[1:], messages[1:]) {
		t.Fatal("recent text rounds changed")
	}
	after, _ := json.Marshal(messages)
	if !bytes.Equal(before, after) {
		t.Fatal("input mutated")
	}
	// An incomplete old batch is protected in addition to five completed rounds.
	calls := compactPolicyPair("r", "a", "bash", "result")
	second := compactPolicyPair("r", "b", "bash", "result")
	calls[0]["tool_calls"] = append(calls[0]["tool_calls"].([]any), second[0]["tool_calls"].([]any)...)
	calls[0]["reasoning_content"] = "pending reasoning"
	all := append(calls, messages[1:]...)
	got = ProjectL1(all, 5, -1, -1, false)
	if got.ToolsCleared != 0 || got.Messages[0]["reasoning_content"] != "pending reasoning" {
		t.Fatal("incomplete batch changed")
	}
	all = append(calls, second[1])
	all = append(all, messages[1:]...)
	got = ProjectL1(all, 5, -1, -1, false)
	if got.ToolsCleared != 2 || got.Messages[0]["content"] != "ordinary assistant text" || len(anyMessageSlice(got.Messages[0]["tool_calls"])) != 0 || got.Messages[1] != nil || got.Messages[2] != nil {
		t.Fatalf("complete old parallel batch not removed atomically: cleared=%d first=%v second=%v third=%v", got.ToolsCleared, got.Messages[0], got.Messages[1], got.Messages[2])
	}
}

func TestActiveL1WritesOnlyCategoryAttributes(t *testing.T) {
	store := newCompactTestStore(t)
	id := "active-category"
	ensureCompactTestChat(t, store, id)
	for i := 0; i < 7; i++ {
		appendCompactTestToolResult(t, store, id, "r", fmt.Sprint(i), "bash", strings.Repeat("output", 100))
	}
	// The helper uses one seq per run. Use distinct runs to model distinct calls.
	// Real active runs use their monotonically increasing seq.
	records, data, err := readJSONLineRecords(store.chatJSONLPath(id))
	if err != nil {
		t.Fatal(err)
	}
	seq := 0
	for _, record := range records {
		if record.Value["_type"] == StepLineTypeReact {
			seq++
			record.Value["seq"] = seq
		}
	}
	var b bytes.Buffer
	for _, record := range records {
		raw, _ := json.Marshal(record.Value)
		b.Write(raw)
		b.WriteByte('\n')
	}
	data = b.Bytes()
	if err := os.WriteFile(store.chatJSONLPath(id), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendRunCompactCheckpoint(id, RunCompactCheckpointLine{Level: "l1_tools", CompactID: "l1", L1KeepRecent: 5}); err != nil {
		t.Fatal(err)
	}
	after, _, err := readJSONLineRecords(store.chatJSONLPath(id))
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(records) {
		t.Fatal("L1 added a JSONL record")
	}
	marked := 0
	for i, record := range after {
		if marker, ok := record.Value["_compact"].(map[string]any); ok {
			marked++
			if marker["level"] != "L1" {
				t.Fatal(marker)
			}
		}
		delete(record.Value, "_compact")
		a, _ := json.Marshal(record.Value)
		b, _ := json.Marshal(records[i].Value)
		if !bytes.Equal(a, b) {
			t.Fatalf("original line %d changed", i)
		}
	}
	if marked != 2 {
		t.Fatalf("marked=%d", marked)
	}
	raw, err := store.LoadRawMessages(id, 20)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, m := range raw {
		if m["role"] == "tool" {
			count++
		}
	}
	if count != 5 {
		t.Fatalf("retained tools=%d", count)
	}
}

func TestActiveSummaryInsertedBeforeRetainedRecords(t *testing.T) {
	store := newCompactTestStore(t)
	id := "active-summary-insert"
	ensureCompactTestChat(t, store, id)
	for i := 1; i <= 3; i++ {
		appendCompactTestRun(t, store, id, fmt.Sprint(i), fmt.Sprint("user", i), fmt.Sprint("answer", i))
	}
	records, _, err := readJSONLineRecords(store.chatJSONLPath(id))
	if err != nil {
		t.Fatal(err)
	}
	raw := rawMessagesFromJSONLLines(recordValues(records))
	var covered, tail []map[string]any
	for _, m := range raw {
		if m["runId"] == "3" {
			tail = append(tail, m)
		} else {
			covered = append(covered, m)
		}
	}
	messages := append([]map[string]any{{"role": "user", "content": CompactCheckpointSummaryMessage("summary")}}, tail...)
	err = store.AppendRunCompactCheckpoint(id, RunCompactCheckpointLine{Level: "summary", CompactID: "sum", RunID: "3", UpdatedAt: testEpochMillis(400), Messages: messages, CompactCoveredMessages: covered})
	if err != nil {
		t.Fatal(err)
	}
	after, _, err := readJSONLineRecords(store.chatJSONLPath(id))
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(records)+1 {
		t.Fatal("expected exactly one summary line")
	}
	summaryIndex, tailIndex := -1, -1
	for i, record := range after {
		if record.Value["compactId"] == "sum" {
			summaryIndex = i
		}
		if record.Value["runId"] == "3" && tailIndex < 0 {
			tailIndex = i
		}
	}
	if summaryIndex < 0 || summaryIndex >= tailIndex {
		t.Fatalf("summary %d tail %d", summaryIndex, tailIndex)
	}
	projected, err := store.LoadRawMessages(id, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(projected) != 3 || !strings.Contains(fmt.Sprint(projected[0]["content"]), "summary") || projected[1]["content"] != "user3" {
		t.Fatalf("bad reload: %v", projected)
	}
}

func TestCompactCategorySchemaRejectsPathsAndPayloadCopies(t *testing.T) {
	for _, marker := range []map[string]any{
		{"level": "L1", "id": "x", "keep": []any{"/messages/0/content"}},
		{"level": "L1", "id": "x", "content": "duplicated"},
		{"level": "L1", "id": "x", "keep": []any{"tool", "tool"}},
		{"level": "L1", "id": ""},
	} {
		if validateCurrentCompactMarkerSchema(map[string]any{"_compact": marker}) == nil {
			t.Fatalf("accepted %v", marker)
		}
	}
	for _, level := range []string{"L0", "L1", "L2"} {
		if err := validateCurrentCompactMarkerSchema(map[string]any{"_compact": map[string]any{"level": level, "id": "x", "keep": []any{"content", "reasoning", "tool"}}}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestL1ExcludingLegacySnapshotDoesNotResurrectOriginals(t *testing.T) {
	store := newCompactTestStore(t)
	id := "legacy-l1-no-resurrection"
	ensureCompactTestChat(t, store, id)
	appendCompactTestToolResult(t, store, id, "old", "obsolete", "bash", "obsolete original")
	var messages []map[string]any
	for i := 0; i < 6; i++ {
		pair := compactPolicyPair("r", fmt.Sprint(i), "bash", "snapshot result")
		delete(pair[0], "content")
		messages = append(messages, pair...)
	}
	if err := store.AppendRunCompactCheckpoint(id, RunCompactCheckpointLine{Type: RunCompactCheckpointLineType, ChatID: id, RunID: "r", CompactID: "old-snapshot", UpdatedAt: testEpochMillis(200), Messages: messages}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		appendCompactTestRun(t, store, id, fmt.Sprint("new-", i), "recent user", "recent answer")
	}
	snapshot, err := store.BuildToolCompactSnapshotToTarget(id, 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ToolsCleared != 6 {
		t.Fatalf("cleared %d", snapshot.ToolsCleared)
	}
	if err := store.CommitToolCompact(id, snapshot, ToolCompactLine{CompactID: "l1"}); err != nil {
		t.Fatal(err)
	}
	raw, err := store.LoadRawMessages(id, 20)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(raw)
	if strings.Contains(string(encoded), "obsolete") || strings.Contains(string(encoded), "snapshot result") || len(raw) != 10 {
		t.Fatal("superseded content restored")
	}
}
