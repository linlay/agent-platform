package chat

import (
	"testing"
	"time"
)

func TestFileStoreListChatsUsesParsedRunIDCursor(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-new", "agent", "", "new"); err != nil {
		t.Fatalf("ensure new chat: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-old", "agent", "", "old"); err != nil {
		t.Fatalf("ensure old chat: %v", err)
	}

	legacyLater := "run_20240101000002.000000000"
	base36Earlier := "loyw3v28"
	if err := store.OnRunCompleted(RunCompletion{
		ChatID:          "chat-new",
		RunID:           legacyLater,
		AssistantText:   "later",
		UpdatedAtMillis: time.Now().UnixMilli(),
	}); err != nil {
		t.Fatalf("complete new chat: %v", err)
	}
	if err := store.OnRunCompleted(RunCompletion{
		ChatID:          "chat-old",
		RunID:           base36Earlier,
		AssistantText:   "earlier",
		UpdatedAtMillis: time.Now().Add(-time.Second).UnixMilli(),
	}); err != nil {
		t.Fatalf("complete old chat: %v", err)
	}

	items, err := store.ListChats("run_20240101000001.000000000", "")
	if err != nil {
		t.Fatalf("list chats with legacy cursor: %v", err)
	}
	if len(items) != 1 || items[0].ChatID != "chat-new" {
		t.Fatalf("expected only later legacy run after cursor, got %#v", items)
	}

	items, err = store.ListChats("loyw3v29", "")
	if err != nil {
		t.Fatalf("list chats with base36 cursor: %v", err)
	}
	if len(items) != 1 || items[0].ChatID != "chat-new" {
		t.Fatalf("expected cross-format cursor comparison to keep later legacy run, got %#v", items)
	}
}

func TestRebuildSnapshotEventsGroupsByRunAndBackfillsLegacyIDs(t *testing.T) {
	run1 := "loyw3v28"
	run2 := "loyw3v2s"

	events := rebuildSnapshotEvents([]map[string]any{
		{"type": "request.query", "chatId": "chat_1", "message": "first"},
		{"type": "chat.start", "chatId": "chat_1", "chatName": "demo"},
		{"type": "run.start", "chatId": "chat_1", "runId": run1},
		{"type": "reasoning.start", "chatId": "chat_1", "runId": run1},
		{"type": "reasoning.end", "chatId": "chat_1", "runId": run1},
		{"type": "reasoning.snapshot", "chatId": "chat_1", "runId": run1, "text": "thinking"},
		{"type": "content.snapshot", "chatId": "chat_1", "runId": run1, "text": "answer 1"},
		{"type": "run.complete", "runId": run1},
		{"type": "request.query", "chatId": "chat_1", "message": "second"},
		{"type": "chat.start", "chatId": "chat_1", "chatName": "demo"},
		{"type": "run.start", "chatId": "chat_1", "runId": run2},
		{"type": "tool.start", "chatId": "chat_1", "runId": run2, "toolName": "_datetime_"},
		{"type": "tool.end", "chatId": "chat_1", "runId": run2},
		{"type": "tool.snapshot", "chatId": "chat_1", "runId": run2, "arguments": "{}"},
		{"type": "tool.result", "chatId": "chat_1", "runId": run2, "output": map[string]any{"ok": true}},
		{"type": "action.start", "chatId": "chat_1", "runId": run2, "actionName": "confirm_dialog"},
		{"type": "action.end", "chatId": "chat_1", "runId": run2},
		{"type": "action.result", "chatId": "chat_1", "runId": run2, "result": map[string]any{"confirmed": true}},
		{"type": "run.complete", "runId": run2},
	})

	if len(events) != 18 {
		t.Fatalf("expected 18 rebuilt events, got %d: %#v", len(events), events)
	}
	if events[0]["type"] != "chat.start" {
		t.Fatalf("expected chat.start first, got %#v", events[0])
	}
	if events[1]["type"] != "request.query" || events[1]["runId"] != run1 {
		t.Fatalf("expected first request.query to bind to run1, got %#v", events[1])
	}
	if events[2]["type"] != "run.start" || events[2]["runId"] != run1 {
		t.Fatalf("expected run1 start after request, got %#v", events[2])
	}
	if events[8]["type"] != "request.query" || events[8]["runId"] != run2 {
		t.Fatalf("expected second request.query to bind to run2, got %#v", events[8])
	}
	if events[9]["type"] != "run.start" || events[9]["runId"] != run2 {
		t.Fatalf("expected run2 start after second request, got %#v", events[9])
	}

	if got := events[3]["reasoningId"]; got != run1+"_r_1" {
		t.Fatalf("expected reasoning.start to backfill run-scoped id, got %#v", events[3])
	}
	if got := events[5]["reasoningId"]; got != run1+"_r_1" {
		t.Fatalf("expected reasoning.snapshot to reuse prior id, got %#v", events[5])
	}
	if got := events[6]["contentId"]; got != run1+"_c_1" {
		t.Fatalf("expected content.snapshot to backfill run-scoped id, got %#v", events[6])
	}

	if got := events[10]["toolId"]; got != run2+"_tool_1" {
		t.Fatalf("expected tool.start fallback id, got %#v", events[10])
	}
	if got := events[11]["toolId"]; got != run2+"_tool_1" {
		t.Fatalf("expected tool.end to reuse fallback id, got %#v", events[11])
	}
	if got := events[12]["toolId"]; got != run2+"_tool_1" {
		t.Fatalf("expected tool.snapshot to reuse fallback id, got %#v", events[12])
	}
	if got := events[13]["toolId"]; got != run2+"_tool_result_1" {
		t.Fatalf("expected tool.result fallback id after closed block, got %#v", events[13])
	}
	if got := events[14]["actionId"]; got != run2+"_action_1" {
		t.Fatalf("expected action.start fallback id, got %#v", events[14])
	}
	if got := events[15]["actionId"]; got != run2+"_action_1" {
		t.Fatalf("expected action.end to reuse fallback id, got %#v", events[15])
	}
	if got := events[16]["actionId"]; got != run2+"_action_result_1" {
		t.Fatalf("expected action.result fallback id after closed block, got %#v", events[16])
	}

	for index, event := range events {
		if got := int64(index + 1); event["seq"] != got {
			t.Fatalf("expected contiguous seq at index %d, got %#v", index, event)
		}
	}
}
