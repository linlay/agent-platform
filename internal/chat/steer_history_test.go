package chat

import (
	"fmt"
	"strings"
	"testing"
)

func TestReferenceOnlySteerHistoryAndLegacySnapshot(t *testing.T) {
	line := map[string]any{
		"_type": "steer", "runId": "run-1", "updatedAt": int64(123),
		"steer": map[string]any{"chatId": "chat-1", "runId": "run-1", "steerId": "steer-1", "role": "user", "message": "", "references": []any{
			map[string]any{"type": "selection", "text": "selected text", "annotation": "fix this", "annotationIndex": 7},
			map[string]any{"type": "file", "url": "image.png", "name": "image.png"},
		}},
	}
	raw := rawMessagesFromJSONLLines([]map[string]any{line})
	if len(raw) != 1 {
		t.Fatalf("lost attachment-only steer: %#v", raw)
	}
	text, _ := raw[0]["content"].(string)
	for _, want := range []string{"selected text", "fix this", "Annotation", "image.png"} {
		if !strings.Contains(strings.ToLower(text), strings.ToLower(want)) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	coordinator := teamCoordinatorRawMessagesFromJSONLLines([]map[string]any{line})
	if len(coordinator) != 1 || coordinator[0][SteerHistoryInputKey] == nil {
		t.Fatalf("coordinator lost steer: %#v", coordinator)
	}
	if member := teamMemberRawMessagesFromJSONLLines([]map[string]any{line}, "member"); len(member) != 0 {
		t.Fatalf("coordinator steer leaked into member history: %#v", member)
	}
	if raw[0][SteerHistoryInputKey] == nil {
		t.Fatal("missing runtime input")
	}
	line["messages"] = []any{map[string]any{"role": "user", "content": "legacy frozen input"}}
	old := llmRequestSteerMessageFromLine(line)
	if old["content"] != "legacy frozen input" || old[SteerHistoryInputKey] != nil {
		t.Fatalf("legacy snapshot changed: %#v", old)
	}
	line["_compact"] = map[string]any{"level": "L1", "id": "compact-1", "keep": []any{"content"}}
	if got := rawMessagesFromJSONLLines([]map[string]any{line}); len(got) != 1 {
		t.Fatalf("L1 lost steer: %#v", got)
	}
}

func TestActiveSummaryMatchesRebuiltSteerReferences(t *testing.T) {
	for _, retained := range []bool{false, true} {
		t.Run(fmt.Sprint("retained=", retained), func(t *testing.T) {
			store := newCompactTestStore(t)
			id := "steer-summary"
			ensureCompactTestChat(t, store, id)
			appendCompactTestRun(t, store, id, "old", "old question", "old answer")
			runID := "old"
			if retained {
				runID = "new"
			}
			err := store.AppendSteerLine(id, SteerLine{Type: "steer", ChatID: id, RunID: runID, UpdatedAt: testEpochMillis(200), Steer: map[string]any{
				"chatId": id, "runId": runID, "steerId": "image", "role": "user", "message": "", "references": []any{map[string]any{"type": "file", "url": "image.png", "name": "image.png"}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			appendCompactTestRun(t, store, id, "new", "new question", "new answer")
			raw, err := store.LoadRawMessages(id, 20)
			if err != nil {
				t.Fatal(err)
			}
			var covered, tail []map[string]any
			for _, m := range raw {
				if m[SteerHistoryInputKey] != nil {
					m["content"] = []map[string]any{{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,current-bytes"}}}
					delete(m, SteerHistoryInputKey)
				}
				if m["runId"] == "old" {
					covered = append(covered, m)
				} else {
					tail = append(tail, m)
				}
			}
			messages := append([]map[string]any{{"role": "user", "content": CompactCheckpointSummaryMessage("summary")}}, tail...)
			if err := store.AppendRunCompactCheckpoint(id, RunCompactCheckpointLine{Level: "summary", CompactID: "sum", RunID: "new", UpdatedAt: testEpochMillis(400), Messages: messages, CompactCoveredMessages: covered}); err != nil {
				t.Fatal(err)
			}
			history, err := store.LoadRawMessages(id, 20)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, m := range history {
				if m[SteerHistoryInputKey] != nil {
					count++
				}
			}
			if (count == 1) != retained {
				t.Fatalf("retained=%v history=%#v", retained, history)
			}
			stored, err := store.LoadJSONLContent(id)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(stored, "current-bytes") {
				t.Fatal("summary copied materialized image into storage")
			}
		})
	}
}
