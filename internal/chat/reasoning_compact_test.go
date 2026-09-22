package chat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReasoningProjectionThresholdAndProtocolProtection(t *testing.T) {
	messages := []map[string]any{
		{"role": "system", "content": "system", "reasoning_content": "untouched"},
		{"role": "assistant", "runId": "old", "content": "answer", "reasoning_content": strings.Repeat("a", 399)},
	}
	before, _ := json.Marshal(messages)
	out, n := CompactReasoningMessages(messages, 100, -1, -1)
	if n != 0 || !reflect.DeepEqual(out, messages) {
		t.Fatal("below threshold changed")
	}
	messages[1]["reasoning_content"] = strings.Repeat("a", 400)
	out, n = CompactReasoningMessages(messages, 100, -1, -1)
	if n != 1 || out[1]["reasoning_content"] != nil || out[1]["content"] != "answer" || out[0]["reasoning_content"] != "untouched" {
		t.Fatal("threshold projection incorrect")
	}
	if len(messages[1]["reasoning_content"].(string)) != 400 || len(before) == 0 {
		t.Fatal("input mutated")
	}
	// A split reasoning entry and a partially completed parallel batch share a scope.
	pending := compactPolicyPair("current", "a", "bash", "first result")
	second := compactPolicyPair("current", "b", "bash", "second result")
	pending[0]["tool_calls"] = append(pending[0]["tool_calls"].([]any), second[0]["tool_calls"].([]any)...)
	messages = append(messages, map[string]any{"role": "assistant", "runId": "current", "reasoning_content": strings.Repeat("r", 400)})
	messages = append(messages, pending...)
	out, n = CompactReasoningMessages(messages, 100, -1, -1)
	if n != 1 || out[2]["reasoning_content"] != messages[2]["reasoning_content"] || !reflect.DeepEqual(out[3:], messages[3:]) {
		t.Fatal("incomplete parallel interaction changed")
	}
	messages = append(messages, second[1])
	out, n = CompactReasoningMessages(messages, 100, 1, 2)
	if n != 1 || out[1]["reasoning_content"] == nil || out[2]["reasoning_content"] != nil {
		t.Fatal("completed interaction or pin mishandled")
	}
}

func TestReasoningHistoryOnlyCompactionBackupAndReplay(t *testing.T) {
	store := newCompactTestStore(t)
	id := "reasoning-history"
	ensureCompactTestChat(t, store, id)
	appendCompactTestRun(t, store, id, "r", "question", "answer")
	records, _, err := readJSONLineRecords(store.chatJSONLPath(id))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range records {
		if r.Value["_type"] == StepLineTypeReact {
			ms := anyMessageSlice(r.Value["messages"])
			for _, m := range ms {
				if m["role"] == "assistant" {
					m["reasoning_content"] = strings.Repeat("reason ", 6000)
				}
			}
		}
	}
	var buffer bytes.Buffer
	for _, r := range records {
		raw, _ := json.Marshal(r.Value)
		buffer.Write(raw)
		buffer.WriteByte('\n')
	}
	if err := os.WriteFile(store.chatJSONLPath(id), buffer.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		appendCompactTestRun(t, store, id, fmt.Sprint("recent-", i), "question", "recent answer")
	}
	original, _ := os.ReadFile(store.chatJSONLPath(id))
	snapshot, err := store.BuildToolCompactSnapshotToTarget(id, 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ReasoningCleared != 1 || snapshot.ToolsCleared != 0 {
		t.Fatalf("snapshot %+v", snapshot)
	}
	if err := store.CommitToolCompact(id, snapshot, ToolCompactLine{CompactID: "l1"}); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(filepath.Join(store.ChatDir(id), ".compact-backups", "l1.jsonl"))
	if err != nil || !bytes.Equal(backup, original) {
		t.Fatal("backup changed")
	}
	after, _ := os.ReadFile(store.chatJSONLPath(id))
	if bytes.Count(after, []byte("\n")) != bytes.Count(original, []byte("\n")) {
		t.Fatal("L1 added lines")
	}
	raw, err := store.LoadRawMessages(id, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range raw {
		if m["runId"] == "r" && m["role"] == "assistant" && (m["reasoning_content"] != nil || m["content"] != "answer") {
			t.Fatal("incorrect projection")
		}
	}
	if !bytes.Contains(after, []byte(strings.Repeat("reason ", 100))) {
		t.Fatal("original reasoning removed")
	}
}

func TestReasoningProjectionDoesNotMatchAnotherActorsToolResult(t *testing.T) {
	pair := compactPolicyPair("r", "same-id", "bash", "result")
	pair[0]["agentKey"] = "a"
	pair[0]["reasoning_content"] = []any{map[string]any{"type": "text", "text": strings.Repeat("r", 400)}}
	pair[1]["agentKey"] = "b"
	out, n := CompactReasoningMessages(pair, 100, -1, -1)
	if n != 0 || !reflect.DeepEqual(out, pair) {
		t.Fatal("another actor released protected reasoning")
	}
	pair[1]["agentKey"] = "a"
	out, n = CompactReasoningMessages(pair, 100, -1, -1)
	if n != 1 || out[0]["reasoning_content"] != nil {
		t.Fatal("matched result did not release reasoning")
	}
}
