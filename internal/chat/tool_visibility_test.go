package chat

import (
	"testing"
)

func TestReplayHidesClientHiddenToolEvents(t *testing.T) {
	summary := Summary{ChatID: "chat-hidden", ChatName: "history", CreatedAt: testEpochMillis(0)}
	call := func(id, name string) map[string]any {
		return map[string]any{"id": id, "_toolId": id, "type": "function", "function": map[string]any{"name": name, "arguments": "{}"}}
	}
	result := func(id, name string) map[string]any {
		return map[string]any{"role": "tool", "name": name, "tool_call_id": id, "_toolId": id, "content": "ok", "ts": testEpochMillis(22)}
	}
	lines := []map[string]any{
		{"_type": "query", "chatId": summary.ChatID, "runId": "run-1", "updatedAt": testEpochMillis(11), "query": map[string]any{"message": "question"}},
		// A failed tool.end is stored before its step and names only the toolId.
		{"_type": "event", "runId": "run-1", "event": map[string]any{"type": "tool.end", "timestamp": testEpochMillis(19), "toolId": "call-announce", "status": "failed"}},
		{"_type": "react", "chatId": summary.ChatID, "runId": "run-1", "updatedAt": testEpochMillis(20), "seq": 1,
			"awaiting": []any{map[string]any{"type": "awaiting.ask", "timestamp": testEpochMillis(21), "awaitingId": "call-question", "mode": "question"}},
			"messages": []any{
				map[string]any{"role": "assistant", "ts": testEpochMillis(20), "tool_calls": []any{
					call("call-announce", "action_announce"),
					call("call-question", "ask_user_question"),
					call("call-delegate", "agent_delegate"),
					call("call-read", "file_read"),
				}},
				result("call-announce", "action_announce"),
				result("call-question", "ask_user_question"),
				result("call-delegate", "agent_delegate"),
				result("call-read", "file_read"),
			}},
	}
	hidden := ClientHiddenToolFunc(func(name string) bool {
		return name == "action_announce" || name == "ask_user_question"
	})
	detail, err := replayChatHistory(summary, lines, nil, t.TempDir(), map[string]int64{"run-1": testEpochMillis(10)}, map[string]int64{"run-1": testEpochMillis(30)}, map[string]string{"run-1": "complete"}, hidden)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	awaitingIndex, readIndex := -1, -1
	for i, event := range detail.Events {
		types = append(types, event.Type)
		if event.Seq != int64(i+1) {
			t.Fatalf("noncontiguous history seq after filtering: %#v", event)
		}
		switch toolID := event.String("toolId"); {
		case event.Type == "awaiting.ask":
			awaitingIndex = i
		case toolID == "call-announce" || toolID == "call-question" || toolID == "call-delegate":
			t.Fatalf("hidden tool event replayed: %#v", event)
		case toolID == "call-read" && event.Type == "tool.snapshot":
			readIndex = i
		}
	}
	want := []string{"chat.start", "request.query", "run.start", "awaiting.ask", "tool.snapshot", "tool.result", "run.complete"}
	if len(types) != len(want) {
		t.Fatalf("event types = %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("event types = %v, want %v", types, want)
		}
	}
	// The question prompt keeps its place beside its (hidden) tool call.
	if awaitingIndex < 0 || readIndex < 0 || awaitingIndex > readIndex {
		t.Fatalf("awaiting.ask moved: awaiting=%d read=%d types=%v", awaitingIndex, readIndex, types)
	}
}

func TestSearchSkipsClientHiddenToolMessages(t *testing.T) {
	hidden := ClientHiddenToolFunc(func(name string) bool { return name == "action_announce" })
	msg := map[string]any{"role": "assistant", "tool_calls": []any{
		map[string]any{"id": "call-announce", "function": map[string]any{"name": "action_announce", "arguments": `{"message":"secret plan"}`}},
	}}
	if text := searchMessageText(msg, hidden); text != "" {
		t.Fatalf("hidden tool call searchable: %q", text)
	}
	if text := searchMessageText(map[string]any{"role": "tool", "name": "action_announce", "content": "announced"}, hidden); text != "" {
		t.Fatalf("hidden tool result searchable: %q", text)
	}
}
