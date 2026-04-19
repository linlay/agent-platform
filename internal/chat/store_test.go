package chat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestStoredMessageToEventsPreservesTimestamp(t *testing.T) {
	const ts int64 = 12345

	testCases := []struct {
		name     string
		msg      map[string]any
		wantType string
	}{
		{
			name: "reasoning snapshot",
			msg: map[string]any{
				"role":              "assistant",
				"ts":                ts,
				"_reasoningId":      "run_1_r_1",
				"reasoning_content": []any{map[string]any{"type": "text", "text": "thinking"}},
			},
			wantType: "reasoning.snapshot",
		},
		{
			name: "content snapshot",
			msg: map[string]any{
				"role":       "assistant",
				"ts":         ts,
				"_contentId": "run_1_c_1",
				"content":    []any{map[string]any{"type": "text", "text": "answer"}},
			},
			wantType: "content.snapshot",
		},
		{
			name: "action snapshot",
			msg: map[string]any{
				"role":      "assistant",
				"ts":        ts,
				"_actionId": "stored-action",
				"tool_calls": []any{
					map[string]any{
						"id": "action-call-1",
						"function": map[string]any{
							"name":      "approval_action",
							"arguments": "{\"approved\":true}",
						},
					},
				},
			},
			wantType: "action.snapshot",
		},
		{
			name: "tool snapshot",
			msg: map[string]any{
				"role":    "assistant",
				"ts":      ts,
				"_toolId": "stored-tool",
				"tool_calls": []any{
					map[string]any{
						"id": "tool-call-1",
						"function": map[string]any{
							"name":      "_datetime_",
							"arguments": "{}",
						},
					},
				},
			},
			wantType: "tool.snapshot",
		},
		{
			name: "action result",
			msg: map[string]any{
				"role":         "tool",
				"ts":           ts,
				"_actionId":    "stored-action",
				"tool_call_id": "action-call-1",
				"content":      "approved",
			},
			wantType: "action.result",
		},
		{
			name: "tool result",
			msg: map[string]any{
				"role":         "tool",
				"ts":           ts,
				"_toolId":      "stored-tool",
				"tool_call_id": "tool-call-1",
				"content":      "ok",
			},
			wantType: "tool.result",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			events := storedMessageToEvents(tc.msg, "run_1", "task_1", "execute", func() int64 { return 1 })
			if len(events) != 1 {
				t.Fatalf("expected one event, got %#v", events)
			}
			if events[0].Type != tc.wantType {
				t.Fatalf("expected %s, got %#v", tc.wantType, events[0])
			}
			if events[0].Timestamp != ts {
				t.Fatalf("expected timestamp %d, got %#v", ts, events[0])
			}
		})
	}
}

func TestLoadChatSynthesizesRunBoundaryTimestamps(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-ts", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	if err := store.AppendQueryLine("chat-ts", QueryLine{
		ChatID:    "chat-ts",
		RunID:     "run-ts",
		UpdatedAt: 1001,
		Query: map[string]any{
			"chatId":  "chat-ts",
			"message": "hello",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}

	if err := store.AppendStepLine("chat-ts", StepLine{
		ChatID:    "chat-ts",
		RunID:     "run-ts",
		UpdatedAt: 1002,
		Type:      "react",
		Seq:       1,
		Messages: []StoredMessage{
			{
				Role:      "assistant",
				Content:   textContent("answer"),
				ContentID: "run-ts_c_1",
				MsgID:     "msg-1",
				Ts:        func() *int64 { v := int64(2002); return &v }(),
			},
		},
	}); err != nil {
		t.Fatalf("append step line: %v", err)
	}

	detail, err := store.LoadChat("chat-ts")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}

	var runStart, runComplete *stream.EventData
	for i := range detail.Events {
		switch detail.Events[i].Type {
		case "run.start":
			runStart = &detail.Events[i]
		case "run.complete":
			runComplete = &detail.Events[i]
		}
	}
	if runStart == nil || runComplete == nil {
		t.Fatalf("expected synthesized run boundaries, got %#v", detail.Events)
	}
	if runStart.Timestamp != 1001 {
		t.Fatalf("expected run.start timestamp 1001, got %#v", runStart)
	}
	if runComplete.Timestamp != 2002 {
		t.Fatalf("expected run.complete timestamp 2002, got %#v", runComplete)
	}
}

func TestStepWriterActionSnapshotPersistsTsAndReplaysTimestamp(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-action-ts", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	writer := NewStepWriter(store, "chat-action-ts", "run-action-ts", "react")
	writer.OnEvent(stream.EventData{
		Type:      "action.snapshot",
		Timestamp: 3456,
		Payload: map[string]any{
			"actionId":   "action-1",
			"actionName": "approval_action",
			"arguments":  "{\"approved\":true}",
		},
	})
	writer.Flush()

	lines, err := readJSONLines(store.chatJSONLPath("chat-action-ts"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected one persisted line, got %#v", lines)
	}

	messages, _ := lines[0]["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("expected one persisted message, got %#v", lines[0])
	}
	msg, _ := messages[0].(map[string]any)
	if got := int64FromAny(msg["ts"]); got != 3456 {
		t.Fatalf("expected persisted ts=3456, got %#v", msg)
	}

	detail, err := store.LoadChat("chat-action-ts")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}

	found := false
	for _, event := range detail.Events {
		if event.Type == "action.snapshot" {
			found = true
			if event.Timestamp != 3456 {
				t.Fatalf("expected replayed action.snapshot timestamp 3456, got %#v", event)
			}
		}
	}
	if !found {
		t.Fatalf("expected action.snapshot in replayed events, got %#v", detail.Events)
	}
}

func TestStepWriterEmbedsAwaitingInStepLine(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	writer := NewStepWriter(store, "chat-awaiting-step", "run-awaiting-step", "react")
	writer.OnEvent(stream.EventData{
		Type:      "tool.snapshot",
		Timestamp: 1001,
		Payload: map[string]any{
			"toolId":    "tool-1",
			"toolName":  "ask_user",
			"arguments": "{\"question\":\"How many?\"}",
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "awaiting.ask",
		Timestamp: 1002,
		Payload: map[string]any{
			"awaitingId": "tool-1",
			"mode":       "question",
			"timeout":    120000,
			"runId":      "run-awaiting-step",
			"questions": []any{
				map[string]any{
					"id":       "q1",
					"question": "How many?",
					"type":     "number",
				},
			},
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "request.submit",
		Timestamp: 1004,
		Payload: map[string]any{
			"requestId":  "req-1",
			"chatId":     "chat-awaiting-step",
			"runId":      "run-awaiting-step",
			"awaitingId": "tool-1",
			"params": []any{
				map[string]any{"question": "How many?", "answer": 3},
			},
		},
	})
	writer.Flush()

	lines, err := readJSONLines(store.chatJSONLPath("chat-awaiting-step"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected one step line and one submit line, got %#v", lines)
	}

	if got := lines[0]["_type"]; got != "react" {
		t.Fatalf("expected first persisted line to be step, got %#v", lines[0])
	}
	awaiting, _ := lines[0]["awaiting"].([]any)
	if len(awaiting) != 1 {
		t.Fatalf("expected embedded awaiting events on step line, got %#v", lines[0])
	}
	for _, raw := range awaiting {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		if _, ok := item["seq"]; ok {
			t.Fatalf("did not expect seq on embedded awaiting item, got %#v", item)
		}
	}
	messages, _ := lines[0]["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("expected one tool snapshot message, got %#v", lines[0])
	}

	if got := lines[1]["_type"]; got != "submit" {
		t.Fatalf("expected second persisted line to be submit, got %#v", lines[1])
	}
	submit, _ := lines[1]["submit"].(map[string]any)
	if submit == nil || submit["type"] != "request.submit" {
		t.Fatalf("expected request.submit submit line, got %#v", lines[1])
	}
	if _, ok := lines[1]["answer"]; ok {
		t.Fatalf("did not expect answer on submit-only line, got %#v", lines[1])
	}
}

func TestStepWriterMergesSubmitAndAnswer(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	writer := NewStepWriter(store, "chat-submit-merge", "run-submit-merge", "react")
	writer.OnEvent(stream.EventData{
		Type:      "request.submit",
		Timestamp: 1001,
		Payload: map[string]any{
			"type":       "request.submit",
			"requestId":  "req-1",
			"chatId":     "chat-submit-merge",
			"runId":      "run-submit-merge",
			"awaitingId": "tool-1",
			"params": []any{
				map[string]any{"question": "How many?", "answer": 3},
			},
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "awaiting.answer",
		Seq:       34,
		Timestamp: 1002,
		Payload: map[string]any{
			"type":       "awaiting.answer",
			"awaitingId": "tool-1",
			"mode":       "question",
			"questions": []any{
				map[string]any{"question": "How many?", "answer": 3},
			},
		},
	})
	writer.Flush()

	lines, err := readJSONLines(store.chatJSONLPath("chat-submit-merge"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected one merged submit line, got %#v", lines)
	}
	if got := lines[0]["_type"]; got != "submit" {
		t.Fatalf("expected submit line, got %#v", lines[0])
	}
	submit, _ := lines[0]["submit"].(map[string]any)
	answer, _ := lines[0]["answer"].(map[string]any)
	if submit == nil || submit["type"] != "request.submit" {
		t.Fatalf("expected merged submit payload, got %#v", lines[0])
	}
	if answer == nil || answer["type"] != "awaiting.answer" {
		t.Fatalf("expected merged answer payload, got %#v", lines[0])
	}
	if _, ok := answer["seq"]; ok {
		t.Fatalf("did not expect seq on merged answer payload, got %#v", answer)
	}
}

func TestStepWriterFormatsStructuredToolResultAsJSON(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	writer := NewStepWriter(store, "chat-tool-result-json", "run-tool-result-json", "react")
	writer.OnEvent(stream.EventData{
		Type:      "tool.snapshot",
		Timestamp: 1001,
		Payload: map[string]any{
			"toolId":    "tool-1",
			"toolName":  "_sandbox_bash_",
			"arguments": `{"command":"echo hi"}`,
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "tool.result",
		Timestamp: 1002,
		Payload: map[string]any{
			"toolId": "tool-1",
			"result": map[string]any{
				"error":    "hitl_timeout",
				"exitCode": -1,
				"output": map[string]any{
					"code":   "hitl_timeout",
					"status": "timeout",
				},
			},
		},
	})
	writer.Flush()

	lines, err := readJSONLines(store.chatJSONLPath("chat-tool-result-json"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected one step line, got %#v", lines)
	}
	messages, _ := lines[0]["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("expected tool snapshot and tool result messages, got %#v", lines[0])
	}
	resultMsg, _ := messages[1].(map[string]any)
	content, _ := resultMsg["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected one content item, got %#v", resultMsg)
	}
	textPart, _ := content[0].(map[string]any)
	text, _ := textPart["text"].(string)
	if strings.Contains(text, "map[") {
		t.Fatalf("expected JSON tool result text, got %q", text)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		t.Fatalf("expected JSON tool result text, got %q err=%v", text, err)
	}
	if decoded["error"] != "hitl_timeout" {
		t.Fatalf("unexpected decoded tool result %#v", decoded)
	}
}

func TestStepWriterEmbedsUsageAtStepLevel(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	writer := NewStepWriter(store, "chat-usage-step", "run-usage-step", "react")
	writer.OnEvent(stream.EventData{
		Type:      "content.snapshot",
		Timestamp: 2001,
		Payload: map[string]any{
			"contentId": "content-1",
			"text":      "hello",
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "debug.postCall",
		Timestamp: 2002,
		Payload: map[string]any{
			"data": map[string]any{
				"contextWindow": map[string]any{
					"max_size":       128000,
					"estimated_size": 200,
				},
				"usage": map[string]any{
					"llmReturnUsage": map[string]any{
						"promptTokens":     100,
						"completionTokens": 50,
						"totalTokens":      150,
					},
				},
			},
		},
	})
	writer.OnEvent(stream.EventData{Type: "run.complete", Timestamp: 2003})

	lines, err := readJSONLines(store.chatJSONLPath("chat-usage-step"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected one step line, got %#v", lines)
	}

	usage, _ := lines[0]["usage"].(map[string]any)
	if toIntValue(usage["prompt_tokens"]) != 100 || toIntValue(usage["total_tokens"]) != 150 {
		t.Fatalf("expected step-level usage, got %#v", lines[0])
	}
	contextWindow, _ := lines[0]["contextWindow"].(map[string]any)
	if toIntValue(contextWindow["max_size"]) != 128000 || toIntValue(contextWindow["actual_size"]) != 100 || toIntValue(contextWindow["estimated_size"]) != 200 {
		t.Fatalf("expected step-level context window, got %#v", lines[0])
	}

	messages, _ := lines[0]["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("expected one stored message, got %#v", lines[0])
	}
	msg, _ := messages[0].(map[string]any)
	if _, ok := msg["_usage"]; ok {
		t.Fatalf("did not expect message-level _usage in new format, got %#v", msg)
	}
	if _, ok := msg["_contextWindow"]; ok {
		t.Fatalf("did not expect message-level _contextWindow in new format, got %#v", msg)
	}
}

func TestStepWriterDropsAwaitingWithoutMessages(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	writer := NewStepWriter(store, "chat-awaiting-drop", "run-awaiting-drop", "react")
	writer.OnEvent(stream.EventData{
		Type:      "awaiting.ask",
		Timestamp: 3001,
		Payload: map[string]any{
			"awaitingId": "tool-1",
			"mode":       "question",
			"timeout":    120000,
		},
	})
	writer.Flush()

	if len(writer.pendingAwaiting) != 0 {
		t.Fatalf("expected pending awaiting to be cleared, got %#v", writer.pendingAwaiting)
	}

	lines, err := readJSONLines(store.chatJSONLPath("chat-awaiting-drop"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 0 {
		t.Fatalf("did not expect persisted lines for dropped awaiting, got %#v", lines)
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

func TestLoadChatReplaysQuestionAwaitLifecycleLegacyEventLines(t *testing.T) {
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
			"type":       "awaiting.ask",
			"awaitingId": "tool-1",
			"mode":       "question",
			"timeout":    120000,
			"runId":      "run-1",
			"questions": []any{
				map[string]any{
					"id":       "q1",
					"question": "How many?",
					"type":     "number",
				},
				map[string]any{
					"id":       "q2",
					"question": "Topics?",
					"type":     "select",
				},
			},
		},
	}); err != nil {
		t.Fatalf("append await ask line: %v", err)
	}

	if err := store.AppendEventLine("chat-1", EventLine{
		ChatID:    "chat-1",
		RunID:     "run-1",
		UpdatedAt: 1003,
		Type:      "event",
		Event: map[string]any{
			"type":       "request.submit",
			"requestId":  "req-1",
			"chatId":     "chat-1",
			"runId":      "run-1",
			"awaitingId": "tool-1",
			"params": []any{
				map[string]any{
					"id":     "q1",
					"answer": 3,
				},
				map[string]any{
					"id":      "q2",
					"answers": []any{"产品更新", "使用教程"},
				},
			},
		},
	}); err != nil {
		t.Fatalf("append request submit line: %v", err)
	}

	if err := store.AppendEventLine("chat-1", EventLine{
		ChatID:    "chat-1",
		RunID:     "run-1",
		UpdatedAt: 1004,
		Type:      "event",
		Event: map[string]any{
			"type":       "awaiting.answer",
			"awaitingId": "tool-1",
			"mode":       "question",
			"answers": []any{
				map[string]any{
					"id":       "q1",
					"question": "How many?",
					"answer":   3,
				},
				map[string]any{
					"id":       "q2",
					"question": "Topics?",
					"answers":  []any{"产品更新", "使用教程"},
				},
			},
		},
	}); err != nil {
		t.Fatalf("append awaiting.answer line: %v", err)
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
		"awaiting.ask",
		"request.submit",
		"awaiting.answer",
		"run.complete",
	}
	for i, eventType := range expectedTypes {
		if detail.Events[i].Type != eventType {
			t.Fatalf("expected event %d to be %s, got %#v", i, eventType, detail.Events[i])
		}
	}

	viewport := detail.Events[3]
	if _, exists := viewport.Payload["awaitName"]; exists {
		t.Fatalf("did not expect awaitName on awaiting.ask replay %#v", viewport)
	}
	if _, exists := viewport.Payload["chatId"]; exists {
		t.Fatalf("did not expect chatId on awaiting.ask replay %#v", viewport)
	}

	questions, _ := viewport.Value("questions").([]any)
	if len(questions) != 2 {
		t.Fatalf("expected inline await ask replay, got %#v", viewport)
	}

	submit := detail.Events[4]
	submitParams, _ := submit.Value("params").([]any)
	if submit.String("awaitingId") != "tool-1" || len(submitParams) != 2 {
		t.Fatalf("unexpected request.submit replay %#v", submit)
	}
	answer := detail.Events[5]
	answerQuestions, _ := answer.Value("answers").([]any)
	if answer.String("awaitingId") != "tool-1" || len(answerQuestions) != 2 {
		t.Fatalf("unexpected awaiting.answer replay %#v", answer)
	}
}

func TestLoadChatReplaysSubmitLine(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-submit-replay", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	if err := store.AppendQueryLine("chat-submit-replay", QueryLine{
		ChatID:    "chat-submit-replay",
		RunID:     "run-submit-replay",
		UpdatedAt: 1000,
		Query: map[string]any{
			"chatId":  "chat-submit-replay",
			"message": "please ask me",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}

	if err := store.AppendSubmitLine("chat-submit-replay", SubmitLine{
		ChatID:    "chat-submit-replay",
		RunID:     "run-submit-replay",
		UpdatedAt: 1001,
		Type:      "submit",
		Submit: map[string]any{
			"type":       "request.submit",
			"requestId":  "req-1",
			"chatId":     "chat-submit-replay",
			"awaitingId": "tool-1",
			"params": []any{
				map[string]any{"question": "How many?", "answer": 3},
			},
		},
		Answer: map[string]any{
			"type":       "awaiting.answer",
			"awaitingId": "tool-1",
			"mode":       "question",
			"questions": []any{
				map[string]any{"question": "How many?", "answer": 3},
			},
		},
	}); err != nil {
		t.Fatalf("append submit line: %v", err)
	}

	detail, err := store.LoadChat("chat-submit-replay")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}

	expectedTypes := []string{
		"chat.start",
		"run.start",
		"request.query",
		"request.submit",
		"awaiting.answer",
		"run.complete",
	}
	if len(detail.Events) != len(expectedTypes) {
		t.Fatalf("expected %d replayed events, got %d: %#v", len(expectedTypes), len(detail.Events), detail.Events)
	}
	for i, eventType := range expectedTypes {
		if detail.Events[i].Type != eventType {
			t.Fatalf("expected event %d to be %s, got %#v", i, eventType, detail.Events[i])
		}
	}
	if detail.Events[3].String("runId") != "run-submit-replay" {
		t.Fatalf("expected runId to be backfilled on request.submit, got %#v", detail.Events[3])
	}
	if detail.Events[4].String("runId") != "run-submit-replay" {
		t.Fatalf("expected runId to be backfilled on awaiting.answer, got %#v", detail.Events[4])
	}
}

func TestLoadChatReplaysAwaitingFromStepLine(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-awaiting-replay", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	if err := store.AppendQueryLine("chat-awaiting-replay", QueryLine{
		ChatID:    "chat-awaiting-replay",
		RunID:     "run-awaiting-replay",
		UpdatedAt: 1000,
		Query: map[string]any{
			"chatId":  "chat-awaiting-replay",
			"message": "please ask me",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}

	toolTs := int64(1001)
	resultTs := int64(1003)
	if err := store.AppendStepLine("chat-awaiting-replay", StepLine{
		ChatID:    "chat-awaiting-replay",
		RunID:     "run-awaiting-replay",
		UpdatedAt: 1004,
		Type:      "react",
		Seq:       1,
		Messages: []StoredMessage{
			{
				Role: "assistant",
				ToolCalls: []StoredToolCall{{
					ID:   "tool-1",
					Type: "function",
					Function: StoredFunction{
						Name:      "ask_user",
						Arguments: "{\"question\":\"How many?\"}",
					},
				}},
				ToolID: "tool-1",
				MsgID:  "msg-1",
				Ts:     &toolTs,
			},
			{
				Role:       "tool",
				Name:       "ask_user",
				ToolCallID: "tool-1",
				Content:    []ContentPart{{Type: "text", Text: "{\"ok\":true}"}},
				ToolID:     "tool-1",
				Ts:         &resultTs,
			},
		},
		Awaiting: []map[string]any{
			{
				"type":       "awaiting.ask",
				"timestamp":  1002,
				"awaitingId": "tool-1",
				"mode":       "question",
				"timeout":    120000,
				"questions": []any{
					map[string]any{
						"id":       "q1",
						"question": "How many?",
						"type":     "number",
					},
				},
			},
		},
	}); err != nil {
		t.Fatalf("append step line: %v", err)
	}

	detail, err := store.LoadChat("chat-awaiting-replay")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}

	expectedTypes := []string{
		"chat.start",
		"run.start",
		"request.query",
		"tool.snapshot",
		"awaiting.ask",
		"tool.result",
		"run.complete",
	}
	if len(detail.Events) != len(expectedTypes) {
		t.Fatalf("expected %d replayed events, got %d: %#v", len(expectedTypes), len(detail.Events), detail.Events)
	}
	for i, eventType := range expectedTypes {
		if detail.Events[i].Type != eventType {
			t.Fatalf("expected event %d to be %s, got %#v", i, eventType, detail.Events[i])
		}
	}
	if detail.Events[4].String("runId") != "run-awaiting-replay" {
		t.Fatalf("expected runId to be backfilled on awaiting.ask, got %#v", detail.Events[4])
	}
	if detail.Events[3].String("toolId") != "tool-1" || detail.Events[4].String("awaitingId") != "tool-1" || detail.Events[5].String("toolId") != "tool-1" {
		t.Fatalf("expected awaiting.ask to be replayed between matching tool snapshot and result, got %#v", detail.Events)
	}
}

func TestLoadChatReplaysAwaitingAfterMatchingToolSnapshotInMultiToolStep(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-awaiting-multi", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	assistantTs := int64(1001)
	toolATs := int64(1002)
	toolBTs := int64(1003)
	if err := store.AppendStepLine("chat-awaiting-multi", StepLine{
		ChatID:    "chat-awaiting-multi",
		RunID:     "run-awaiting-multi",
		UpdatedAt: 1005,
		Type:      "react",
		Seq:       1,
		Messages: []StoredMessage{
			{
				Role: "assistant",
				ToolCalls: []StoredToolCall{
					{
						ID:   "tool-a",
						Type: "function",
						Function: StoredFunction{
							Name:      "ask_a",
							Arguments: "{\"question\":\"A?\"}",
						},
					},
					{
						ID:   "tool-b",
						Type: "function",
						Function: StoredFunction{
							Name:      "ask_b",
							Arguments: "{\"question\":\"B?\"}",
						},
					},
				},
				MsgID: "msg-1",
				Ts:    &assistantTs,
			},
			{
				Role:       "tool",
				Name:       "ask_a",
				ToolCallID: "tool-a",
				Content:    []ContentPart{{Type: "text", Text: "done-a"}},
				ToolID:     "tool-a",
				Ts:         &toolATs,
			},
			{
				Role:       "tool",
				Name:       "ask_b",
				ToolCallID: "tool-b",
				Content:    []ContentPart{{Type: "text", Text: "done-b"}},
				ToolID:     "tool-b",
				Ts:         &toolBTs,
			},
		},
		Awaiting: []map[string]any{
			{
				"type":       "awaiting.ask",
				"timestamp":  1001,
				"awaitingId": "tool-b",
				"mode":       "question",
				"questions":  []any{map[string]any{"id": "qb", "question": "B?"}},
			},
			{
				"type":       "awaiting.ask",
				"timestamp":  1001,
				"awaitingId": "tool-a",
				"mode":       "question",
				"questions":  []any{map[string]any{"id": "qa", "question": "A?"}},
			},
		},
	}); err != nil {
		t.Fatalf("append step line: %v", err)
	}

	detail, err := store.LoadChat("chat-awaiting-multi")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}

	expectedTypes := []string{
		"chat.start",
		"run.start",
		"tool.snapshot",
		"awaiting.ask",
		"tool.snapshot",
		"awaiting.ask",
		"tool.result",
		"tool.result",
		"run.complete",
	}
	if len(detail.Events) != len(expectedTypes) {
		t.Fatalf("expected %d replayed events, got %d: %#v", len(expectedTypes), len(detail.Events), detail.Events)
	}
	for i, eventType := range expectedTypes {
		if detail.Events[i].Type != eventType {
			t.Fatalf("expected event %d to be %s, got %#v", i, eventType, detail.Events[i])
		}
	}
	if detail.Events[2].String("toolId") != "tool-a" || detail.Events[3].String("awaitingId") != "tool-a" {
		t.Fatalf("expected tool-a awaiting.ask immediately after snapshot, got %#v", detail.Events)
	}
	if detail.Events[4].String("toolId") != "tool-b" || detail.Events[5].String("awaitingId") != "tool-b" {
		t.Fatalf("expected tool-b awaiting.ask immediately after snapshot, got %#v", detail.Events)
	}
}

func TestLoadChatReplaysUnmatchedAwaitingAtStepEnd(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-awaiting-fallback", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	toolTs := int64(1001)
	resultTs := int64(1002)
	if err := store.AppendStepLine("chat-awaiting-fallback", StepLine{
		ChatID:    "chat-awaiting-fallback",
		RunID:     "run-awaiting-fallback",
		UpdatedAt: 1004,
		Type:      "react",
		Seq:       1,
		Messages: []StoredMessage{
			{
				Role: "assistant",
				ToolCalls: []StoredToolCall{{
					ID:   "tool-1",
					Type: "function",
					Function: StoredFunction{
						Name:      "ask_user",
						Arguments: "{\"question\":\"How many?\"}",
					},
				}},
				ToolID: "tool-1",
				MsgID:  "msg-1",
				Ts:     &toolTs,
			},
			{
				Role:       "tool",
				Name:       "ask_user",
				ToolCallID: "tool-1",
				Content:    []ContentPart{{Type: "text", Text: "done"}},
				ToolID:     "tool-1",
				Ts:         &resultTs,
			},
		},
		Awaiting: []map[string]any{
			{
				"type":       "awaiting.ask",
				"timestamp":  1003,
				"awaitingId": "tool-missing",
				"mode":       "question",
				"questions":  []any{map[string]any{"id": "q1", "question": "How many?"}},
			},
		},
	}); err != nil {
		t.Fatalf("append step line: %v", err)
	}

	detail, err := store.LoadChat("chat-awaiting-fallback")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}

	expectedTypes := []string{
		"chat.start",
		"run.start",
		"tool.snapshot",
		"tool.result",
		"awaiting.ask",
		"run.complete",
	}
	if len(detail.Events) != len(expectedTypes) {
		t.Fatalf("expected %d replayed events, got %d: %#v", len(expectedTypes), len(detail.Events), detail.Events)
	}
	for i, eventType := range expectedTypes {
		if detail.Events[i].Type != eventType {
			t.Fatalf("expected event %d to be %s, got %#v", i, eventType, detail.Events[i])
		}
	}
	if detail.Events[4].String("awaitingId") != "tool-missing" {
		t.Fatalf("expected unmatched awaiting.ask to be preserved at step end, got %#v", detail.Events[4])
	}
}

func TestLoadChatReplaysSteerLine(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-steer", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	if err := store.AppendQueryLine("chat-steer", QueryLine{
		ChatID:    "chat-steer",
		RunID:     "run-steer",
		UpdatedAt: 1000,
		Query: map[string]any{
			"chatId":  "chat-steer",
			"message": "hello",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}

	if err := store.AppendEventLine("chat-steer", EventLine{
		ChatID:    "chat-steer",
		RunID:     "run-steer",
		UpdatedAt: 1001,
		Type:      "steer",
		Event: map[string]any{
			"type":      "request.steer",
			"requestId": "req-steer",
			"chatId":    "chat-steer",
			"steerId":   "steer-1",
			"message":   "focus on the root cause",
		},
	}); err != nil {
		t.Fatalf("append steer line: %v", err)
	}

	detail, err := store.LoadChat("chat-steer")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}

	expectedTypes := []string{
		"chat.start",
		"run.start",
		"request.query",
		"request.steer",
		"run.complete",
	}
	if len(detail.Events) != len(expectedTypes) {
		t.Fatalf("expected %d replayed events, got %d: %#v", len(expectedTypes), len(detail.Events), detail.Events)
	}
	for i, eventType := range expectedTypes {
		if detail.Events[i].Type != eventType {
			t.Fatalf("expected event %d to be %s, got %#v", i, eventType, detail.Events[i])
		}
	}
	if detail.Events[3].String("runId") != "run-steer" {
		t.Fatalf("expected runId to be backfilled on request.steer, got %#v", detail.Events[3])
	}
}

func TestLoadChatReadsUsageFromStepLevel(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-step-usage", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	if err := store.AppendQueryLine("chat-step-usage", QueryLine{
		ChatID:    "chat-step-usage",
		RunID:     "run-step-usage",
		UpdatedAt: 1000,
		Query: map[string]any{
			"chatId":  "chat-step-usage",
			"message": "hello",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}

	contentTs := int64(1002)
	if err := store.AppendStepLine("chat-step-usage", StepLine{
		ChatID:    "chat-step-usage",
		RunID:     "run-step-usage",
		UpdatedAt: 1003,
		Type:      "react",
		Seq:       1,
		Messages: []StoredMessage{
			{
				Role:      "assistant",
				Content:   textContent("answer"),
				ContentID: "content-1",
				MsgID:     "msg-1",
				Ts:        &contentTs,
			},
		},
		Usage: map[string]any{
			"prompt_tokens":     100,
			"completion_tokens": 50,
			"total_tokens":      150,
		},
		ContextWindow: map[string]any{
			"max_size":       128000,
			"actual_size":    100,
			"estimated_size": 200,
		},
	}); err != nil {
		t.Fatalf("append step line: %v", err)
	}

	detail, err := store.LoadChat("chat-step-usage")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}

	expectedTypes := []string{
		"chat.start",
		"run.start",
		"request.query",
		"debug.preCall",
		"content.snapshot",
		"debug.postCall",
		"run.complete",
	}
	if len(detail.Events) != len(expectedTypes) {
		t.Fatalf("expected %d replayed events, got %d: %#v", len(expectedTypes), len(detail.Events), detail.Events)
	}
	for i, eventType := range expectedTypes {
		if detail.Events[i].Type != eventType {
			t.Fatalf("expected event %d to be %s, got %#v", i, eventType, detail.Events[i])
		}
	}

	preCallData, _ := detail.Events[3].Value("data").(map[string]any)
	preCallCW, _ := preCallData["contextWindow"].(map[string]any)
	if toIntValue(preCallCW["max_size"]) != 128000 || toIntValue(preCallCW["actual_size"]) != 100 || toIntValue(preCallCW["estimated_size"]) != 200 {
		t.Fatalf("unexpected debug.preCall context window %#v", detail.Events[3])
	}

	postCallData, _ := detail.Events[5].Value("data").(map[string]any)
	postCallUsage, _ := postCallData["usage"].(map[string]any)
	llmUsage, _ := postCallUsage["llmReturnUsage"].(map[string]any)
	if toIntValue(llmUsage["promptTokens"]) != 100 || toIntValue(llmUsage["completionTokens"]) != 50 || toIntValue(llmUsage["totalTokens"]) != 150 {
		t.Fatalf("unexpected debug.postCall usage %#v", detail.Events[5])
	}
}

func TestLoadChatReplaysApprovalAwaitLifecycleLegacyEventLines(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-approval", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	if err := store.AppendQueryLine("chat-approval", QueryLine{
		ChatID:    "chat-approval",
		RunID:     "run-approval",
		UpdatedAt: 1000,
		Query: map[string]any{
			"chatId":  "chat-approval",
			"message": "please approve",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}

	if err := store.AppendEventLine("chat-approval", EventLine{
		ChatID:    "chat-approval",
		RunID:     "run-approval",
		UpdatedAt: 1001,
		Type:      "event",
		Event: map[string]any{
			"type":       "awaiting.ask",
			"awaitingId": "tool-approval",
			"mode":       "approval",
			"timeout":    120000,
			"runId":      "run-approval",
			"approvals": []any{
				map[string]any{
					"id":      "cmd-1",
					"command": "Proceed?",
					"level":   1,
				},
			},
		},
	}); err != nil {
		t.Fatalf("append approval await ask line: %v", err)
	}

	if err := store.AppendEventLine("chat-approval", EventLine{
		ChatID:    "chat-approval",
		RunID:     "run-approval",
		UpdatedAt: 1002,
		Type:      "event",
		Event: map[string]any{
			"type":       "request.submit",
			"requestId":  "req-approval",
			"chatId":     "chat-approval",
			"runId":      "run-approval",
			"awaitingId": "tool-approval",
			"params": []any{
				map[string]any{"id": "cmd-1", "decision": "approve"},
			},
		},
	}); err != nil {
		t.Fatalf("append approval request submit line: %v", err)
	}

	if err := store.AppendEventLine("chat-approval", EventLine{
		ChatID:    "chat-approval",
		RunID:     "run-approval",
		UpdatedAt: 1003,
		Type:      "event",
		Event: map[string]any{
			"type":       "awaiting.answer",
			"awaitingId": "tool-approval",
			"mode":       "approval",
			"approvals": []any{
				map[string]any{"id": "cmd-1", "command": "Proceed?", "decision": "approve"},
			},
		},
	}); err != nil {
		t.Fatalf("append approval awaiting.answer line: %v", err)
	}

	detail, err := store.LoadChat("chat-approval")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}

	foundAwaitAsk := false
	foundAwaitAnswer := false
	for _, event := range detail.Events {
		switch event.Type {
		case "awaiting.ask":
			foundAwaitAsk = true
			approvals, _ := event.Value("approvals").([]any)
			if len(approvals) != 1 {
				t.Fatalf("expected approval awaiting.ask approvals length 1, got %#v", event)
			}
		case "awaiting.answer":
			foundAwaitAnswer = true
			approvals, _ := event.Value("approvals").([]any)
			if event.String("mode") != "approval" || len(approvals) != 1 {
				t.Fatalf("unexpected approval awaiting.answer %#v", event)
			}
		}
	}
	if !foundAwaitAsk {
		t.Fatalf("expected approval awaiting.ask replay, got %#v", detail.Events)
	}
	if !foundAwaitAnswer {
		t.Fatalf("expected approval awaiting.answer replay, got %#v", detail.Events)
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
			"timeout":      120000,
			"runId":        "run-legacy",
			"chatId":       "chat-legacy",
		},
		{
			"type":      "confirm.payload",
			"confirmId": "tool-legacy",
			"payload":   map[string]any{"mode": "approval"},
		},
		{
			"type":       "request.submit",
			"requestId":  "req-legacy",
			"chatId":     "chat-legacy",
			"runId":      "run-legacy",
			"awaitingId": "tool-legacy",
			"params":     map[string]any{"value": "approve"},
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
