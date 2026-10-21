package chat

import (
	"testing"

	"agent-platform/internal/stream"
)

func TestStepWriterPersistsToolUsageBesideAgentUsage(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	writer := NewStepWriter(store, "chat-tool-usage", "run-tool-usage", "react")
	onEventForTest(writer, stream.EventData{
		Type:      "content.snapshot",
		Timestamp: testEpochMillis(3001),
		Payload:   map[string]any{"contentId": "content-1", "text": "drawing"},
	})
	onEventForTest(writer, stream.EventData{
		Type:      "usage.snapshot",
		Timestamp: testEpochMillis(3002),
		Payload: map[string]any{
			"contextWindow": map[string]any{"maxSize": 128000, "currentSize": 100, "modelKey": "chat-model"},
			"usage": map[string]any{
				"current": map[string]any{"modelKey": "chat-model", "promptTokens": 100, "completionTokens": 20, "totalTokens": 120, "llmChatCompletionCount": 1},
			},
		},
	})
	onEventForTest(writer, stream.EventData{
		Type:      "usage.snapshot",
		Timestamp: testEpochMillis(3003),
		Payload: map[string]any{
			"contextWindow": map[string]any{"maxSize": 128000, "currentSize": 100, "modelKey": "chat-model"},
			"usage": map[string]any{
				"current": map[string]any{
					"source": "tool", "toolId": "tool_1", "toolName": "image_generate", "operation": "image.generation",
					"modelKey": "image-model", "promptTokens": 12, "completionTokens": 1000, "totalTokens": 1012,
					"estimatedCost": map[string]any{"currency": "CNY", "inputCacheMiss": 0.1, "output": 0.2, "total": 0.3},
				},
			},
		},
	})
	onEventForTest(writer, stream.EventData{Type: "run.complete", Timestamp: testEpochMillis(3004)})

	lines, err := readJSONLines(store.chatJSONLPath("chat-tool-usage"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected one step line, got %#v", lines)
	}
	line := lines[0]
	usage, _ := line["usage"].(map[string]any)
	if toIntValue(usage["totalTokens"]) != 120 || line["modelKey"] != "chat-model" {
		t.Fatalf("tool usage must not replace agent step usage or model metadata, got %#v", line)
	}
	contextWindow, _ := line["contextWindow"].(map[string]any)
	if toIntValue(contextWindow["currentSize"]) != 100 {
		t.Fatalf("unexpected context window %#v", contextWindow)
	}
	toolUsage, _ := line["toolUsage"].([]any)
	if len(toolUsage) != 1 {
		t.Fatalf("expected one tool usage entry, got %#v", line)
	}
	entry, _ := toolUsage[0].(map[string]any)
	cost, _ := entry["estimatedCost"].(map[string]any)
	if entry["modelKey"] != "image-model" || entry["toolId"] != "tool_1" || entry["operation"] != "image.generation" ||
		toIntValue(entry["totalTokens"]) != 1012 || cost["total"] != 0.3 {
		t.Fatalf("unexpected tool usage entry %#v", entry)
	}
	if _, ok := entry["llmChatCompletionCount"]; ok {
		t.Fatalf("tool usage is not an agent model call: %#v", entry)
	}
}

func TestLoadChatReplaysToolUsageIntoTotals(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-replay-tool", "agent", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := appendQueryLineForTest(store, "chat-replay-tool", QueryLine{
		ChatID: "chat-replay-tool", RunID: "run-replay-tool", UpdatedAt: testEpochMillis(1000),
		Query: map[string]any{"chatId": "chat-replay-tool", "message": "hello"}, Type: "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}
	contentTs := int64(1002)
	if err := appendStepLineForTest(store, "chat-replay-tool", StepLine{
		ChatID: "chat-replay-tool", RunID: "run-replay-tool", UpdatedAt: testEpochMillis(1003),
		ModelKey: "chat-model", Type: "react", Seq: 1,
		Messages: []StoredMessage{{Role: "assistant", Content: textContent("answer"), ContentID: "content-1", MsgID: "msg-1", Ts: &contentTs}},
		Usage: map[string]any{
			"promptTokens": 100, "completionTokens": 20, "totalTokens": 120, "llmChatCompletionCount": 1,
			"estimatedCost": map[string]any{"currency": "CNY", "inputCacheMiss": 0.01, "output": 0.02, "total": 0.03},
		},
		ToolUsage: []map[string]any{
			{"modelKey": "image-model", "toolId": "tool_1", "promptTokens": 12, "completionTokens": 1000, "totalTokens": 1012,
				"estimatedCost": map[string]any{"currency": "CNY", "inputCacheMiss": 0.1, "output": 0.2, "total": 0.3}},
			{"modelKey": "image-model", "toolId": "tool_2", "promptTokens": 8, "completionTokens": 500, "totalTokens": 508,
				"estimatedCost": map[string]any{"currency": "CNY", "inputCacheMiss": 0.05, "output": 0.1, "total": 0.15}},
		},
		ContextWindow: map[string]any{"maxSize": 128000, "currentSize": 100},
	}); err != nil {
		t.Fatalf("append step line: %v", err)
	}
	if err := completeRunForTest(store, RunCompletion{ChatID: "chat-replay-tool", RunID: "run-replay-tool", UpdatedAtMillis: testEpochMillis(1004)}); err != nil {
		t.Fatalf("complete run: %v", err)
	}

	detail, err := store.LoadChat("chat-replay-tool")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}
	for name, usage := range map[string]UsageData{"lastRun": detail.ReplayUsage.LastRun, "chat": detail.ReplayUsage.Chat} {
		if usage.PromptTokens != 120 || usage.CompletionTokens != 1520 || usage.TotalTokens != 1640 || usage.LlmChatCompletionCount != 1 {
			t.Fatalf("%s replay tokens must include tool usage once, got %#v", name, usage)
		}
		if usage.EstimatedCostCurrency != "CNY" || !approxEqual(usage.EstimatedCostTotal, 0.48) {
			t.Fatalf("%s replay cost must include tool usage, got %#v", name, usage)
		}
	}
	if detail.ContextWindow["modelKey"] != "chat-model" || toIntValue(detail.ContextWindow["currentSize"]) != 100 {
		t.Fatalf("tool usage must not change the replayed context window, got %#v", detail.ContextWindow)
	}
}

func TestCharacterBilledToolUsagePersistsAndReplaysCost(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-speech", "agent", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := appendQueryLineForTest(store, "chat-speech", QueryLine{
		ChatID: "chat-speech", RunID: "run-speech", UpdatedAt: testEpochMillis(1000),
		Query: map[string]any{"chatId": "chat-speech", "message": "read it"}, Type: "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}
	writer := NewStepWriter(store, "chat-speech", "run-speech", "react")
	onEventForTest(writer, stream.EventData{
		Type: "content.snapshot", Timestamp: testEpochMillis(1001),
		Payload: map[string]any{"contentId": "content-1", "text": "here"},
	})
	onEventForTest(writer, stream.EventData{
		Type: "usage.snapshot", Timestamp: testEpochMillis(1002),
		Payload: map[string]any{"usage": map[string]any{"current": map[string]any{
			"modelKey": "chat-model", "promptTokens": 100, "completionTokens": 20, "totalTokens": 120, "llmChatCompletionCount": 1,
		}}},
	})
	onEventForTest(writer, stream.EventData{
		Type: "usage.snapshot", Timestamp: testEpochMillis(1003),
		Payload: map[string]any{"usage": map[string]any{"current": map[string]any{
			"source": "tool", "toolId": "tool_1", "toolName": "speech_generate", "operation": "speech.generation",
			"modelKey": "speech-model", "promptTokens": 0, "completionTokens": 0, "totalTokens": 0, "characters": 2000,
			"estimatedCost": map[string]any{"currency": "CNY", "total": 0.7},
		}}},
	})
	onEventForTest(writer, stream.EventData{Type: "run.complete", Timestamp: testEpochMillis(1004)})

	lines, err := readJSONLines(store.chatJSONLPath("chat-speech"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	var entry map[string]any
	for _, line := range lines {
		if toolUsage, _ := line["toolUsage"].([]any); len(toolUsage) == 1 {
			entry, _ = toolUsage[0].(map[string]any)
		}
	}
	if entry == nil || toIntValue(entry["characters"]) != 2000 || entry["operation"] != "speech.generation" {
		t.Fatalf("character tool usage not persisted: %#v", lines)
	}
	if err := completeRunForTest(store, RunCompletion{ChatID: "chat-speech", RunID: "run-speech", UpdatedAtMillis: testEpochMillis(1005)}); err != nil {
		t.Fatalf("complete run: %v", err)
	}
	detail, err := store.LoadChat("chat-speech")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}
	usage := detail.ReplayUsage.LastRun
	if usage.TotalTokens != 120 || usage.EstimatedCostCurrency != "CNY" || !approxEqual(usage.EstimatedCostTotal, 0.7) {
		t.Fatalf("replay must add character cost without tokens, got %#v", usage)
	}
}

func approxEqual(a, b float64) bool {
	diff := a - b
	return diff < 1e-9 && diff > -1e-9
}
