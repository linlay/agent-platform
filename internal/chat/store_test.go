package chat

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"agent-platform-runner-go/internal/stream"
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
		{"type": "action.start", "chatId": "chat_1", "runId": run2, "actionName": "approval_action"},
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
	if got := events[3]["reasoningLabel"]; got != stream.ReasoningLabelForID(run1+"_r_1") {
		t.Fatalf("expected reasoning.start to backfill reasoningLabel, got %#v", events[3])
	}
	if got := events[5]["reasoningId"]; got != run1+"_r_1" {
		t.Fatalf("expected reasoning.snapshot to reuse prior id, got %#v", events[5])
	}
	if got := events[5]["reasoningLabel"]; got != stream.ReasoningLabelForID(run1+"_r_1") {
		t.Fatalf("expected reasoning.snapshot to reuse deterministic reasoningLabel, got %#v", events[5])
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

func TestStoredMessageToEventsAddsReasoningLabel(t *testing.T) {
	runID := "run_1"
	events := storedMessageToEvents(map[string]any{
		"role":              "assistant",
		"_reasoningId":      runID + "_r_2",
		"reasoning_content": []any{map[string]any{"type": "text", "text": "thinking"}},
	}, runID, "task_1", "plan", func() int64 { return 1 })

	if len(events) != 1 {
		t.Fatalf("expected one event, got %#v", events)
	}
	if events[0].Type != "reasoning.snapshot" {
		t.Fatalf("expected reasoning.snapshot, got %#v", events[0])
	}
	if got := events[0].Payload["reasoningLabel"]; got != stream.ReasoningLabelForID(runID+"_r_2") {
		t.Fatalf("expected reasoningLabel in storedMessageToEvents, got %#v", events[0].Payload)
	}
}

func TestLoadRawMessagesFallsBackToLegacyFile(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if err := os.MkdirAll(store.ChatDir("chat-1"), 0o755); err != nil {
		t.Fatalf("create chat dir: %v", err)
	}
	legacyPath := filepath.Join(store.ChatDir("chat-1"), "raw_messages.jsonl")
	content := "{\"role\":\"user\",\"content\":\"hello\",\"runId\":\"run-1\"}\n{\"role\":\"assistant\",\"content\":\"world\",\"runId\":\"run-1\"}\n"
	if err := os.WriteFile(legacyPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write legacy raw messages: %v", err)
	}

	messages, err := store.LoadRawMessages("chat-1", 5)
	if err != nil {
		t.Fatalf("load raw messages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected legacy fallback messages, got %#v", messages)
	}
	if messages[1]["content"] != "world" {
		t.Fatalf("expected assistant message from legacy fallback, got %#v", messages)
	}
}

func TestLoadChatReplaysEventLinesForAwaitLifecycle(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-1", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	if err := store.AppendQueryLine("chat-1", QueryLine{
		ChatID:    "chat-1",
		RunID:     "run-1",
		UpdatedAt: 1000,
		Query: map[string]any{
			"chatId":  "chat-1",
			"message": "please ask me",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}

	if err := store.AppendEventLine("chat-1", EventLine{
		ChatID:    "chat-1",
		RunID:     "run-1",
		UpdatedAt: 1001,
		Type:      "event",
		Event: map[string]any{
			"type":         "await.question",
			"awaitId":      "tool-1",
			"awaitName":    "_ask_user_question_",
			"viewportType": "builtin",
			"viewportKey":  "confirm_dialog",
			"mode":         "question",
			"toolTimeout":  120000,
			"chatId":       "chat-1",
			"runId":        "run-1",
		},
	}); err != nil {
		t.Fatalf("append await question line: %v", err)
	}

	if err := store.AppendEventLine("chat-1", EventLine{
		ChatID:    "chat-1",
		RunID:     "run-1",
		UpdatedAt: 1002,
		Type:      "event",
		Event: map[string]any{
			"type":    "await.payload",
			"awaitId": "tool-1",
			"payload": map[string]any{
				"mode": "question",
				"questions": []any{
					map[string]any{
						"question": "How many?",
						"type":     "number",
					},
				},
			},
		},
	}); err != nil {
		t.Fatalf("append await payload line: %v", err)
	}

	if err := store.AppendEventLine("chat-1", EventLine{
		ChatID:    "chat-1",
		RunID:     "run-1",
		UpdatedAt: 1003,
		Type:      "event",
		Event: map[string]any{
			"type":      "await.answer",
			"requestId": "req-1",
			"chatId":    "chat-1",
			"runId":     "run-1",
			"toolId":    "tool-1",
			"payload": map[string]any{
				"answers": []any{
					map[string]any{
						"question": "How many?",
						"answer":   3,
					},
				},
			},
		},
	}); err != nil {
		t.Fatalf("append await answer line: %v", err)
	}

	detail, err := store.LoadChat("chat-1")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}

	if len(detail.Events) != 7 {
		t.Fatalf("expected 7 replayed events, got %d: %#v", len(detail.Events), detail.Events)
	}

	expectedTypes := []string{
		"chat.start",
		"run.start",
		"request.query",
		"await.question",
		"await.payload",
		"await.answer",
		"run.complete",
	}
	for i, eventType := range expectedTypes {
		if detail.Events[i].Type != eventType {
			t.Fatalf("expected event %d to be %s, got %#v", i, eventType, detail.Events[i])
		}
	}

	viewport := detail.Events[3]
	if viewport.String("awaitName") != "_ask_user_question_" || viewport.String("viewportKey") != "confirm_dialog" {
		t.Fatalf("unexpected await question replay %#v", viewport)
	}

	payload := detail.Events[4]
	payloadMap, _ := payload.Value("payload").(map[string]any)
	if payloadMap["mode"] != "question" {
		t.Fatalf("expected await payload replay, got %#v", payload)
	}

	submit := detail.Events[5]
	submitPayload, _ := submit.Value("payload").(map[string]any)
	if submit.String("toolId") != "tool-1" || submitPayload == nil {
		t.Fatalf("unexpected await.answer replay %#v", submit)
	}
}

func TestLoadChatReplaysLegacyConfirmLifecycleEvents(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-legacy", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	if err := store.AppendQueryLine("chat-legacy", QueryLine{
		ChatID:    "chat-legacy",
		RunID:     "run-legacy",
		UpdatedAt: 1000,
		Query: map[string]any{
			"chatId":  "chat-legacy",
			"message": "legacy confirm flow",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}

	legacyEvents := []map[string]any{
		{
			"type":         "confirm.viewport",
			"confirmId":    "tool-legacy",
			"confirmName":  "_ask_user_approval_",
			"viewportType": "builtin",
			"viewportKey":  "confirm_dialog",
			"mode":         "approval",
			"toolTimeout":  120000,
			"runId":        "run-legacy",
			"chatId":       "chat-legacy",
		},
		{
			"type":      "confirm.payload",
			"confirmId": "tool-legacy",
			"payload":   map[string]any{"mode": "approval"},
		},
		{
			"type":      "request.submit",
			"requestId": "req-legacy",
			"chatId":    "chat-legacy",
			"runId":     "run-legacy",
			"toolId":    "tool-legacy",
			"payload":   map[string]any{"value": "approve"},
		},
	}
	for _, event := range legacyEvents {
		if err := store.AppendEventLine("chat-legacy", EventLine{
			ChatID:    "chat-legacy",
			RunID:     "run-legacy",
			UpdatedAt: 1001,
			Type:      "event",
			Event:     event,
		}); err != nil {
			t.Fatalf("append legacy event line: %v", err)
		}
	}

	detail, err := store.LoadChat("chat-legacy")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}

	foundConfirmViewport := false
	foundConfirmPayload := false
	foundRequestSubmit := false
	for _, event := range detail.Events {
		switch event.Type {
		case "confirm.viewport":
			foundConfirmViewport = true
		case "confirm.payload":
			foundConfirmPayload = true
		case "request.submit":
			foundRequestSubmit = true
		}
	}
	if !foundConfirmViewport || !foundConfirmPayload || !foundRequestSubmit {
		t.Fatalf("expected legacy confirm lifecycle events to replay, got %#v", detail.Events)
	}
}
