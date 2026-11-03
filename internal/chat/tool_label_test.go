package chat

import (
	"testing"

	"agent-platform/internal/stream"
)

func TestStepWriterPersistsToolLabelForReplay(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	defer store.Close()
	const (
		chatID  = "chat-tool-label"
		runID   = "run-tool-label"
		mcpName = "mcp_a06a12877776ca4b_datascope_query"
	)
	if _, _, err := store.EnsureChat(chatID, "agent", "list databases"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := ensureRunStartedForTest(store, chatID, runID, testEpochMillis(20)); err != nil {
		t.Fatalf("start run: %v", err)
	}
	writer := NewStepWriter(store, chatID, runID, "REACT")
	dispatcher := stream.NewDispatcher(stream.StreamRequest{ChatID: chatID, RunID: runID})
	write := func(input stream.StreamInput) {
		for _, event := range dispatcher.Dispatch(input) {
			writer.OnEvent(event.Data())
		}
	}
	calls := []struct{ id, name, label string }{
		{"call-mcp", mcpName, "datascope_query"},
		{"call-read", "file_read", "file_read"},
	}
	for _, call := range calls {
		write(stream.ToolArgs{ToolID: call.id, ToolName: call.name, ToolLabel: call.label, Delta: "{}"})
		write(stream.ToolEnd{ToolID: call.id})
	}
	writer.CommitModelTurn("", 2)
	for _, call := range calls {
		write(stream.ToolResult{ToolID: call.id, ToolName: call.name, ToolLabel: call.label, Result: "ok"})
	}
	writer.Flush()
	if err := writer.Err(); err != nil {
		t.Fatal(err)
	}

	lines, err := readJSONLines(store.chatJSONLPath(chatID))
	if err != nil {
		t.Fatal(err)
	}
	stored := map[string]any{}
	for _, line := range lines {
		for _, message := range messageMapsFromAny(line["messages"]) {
			for _, call := range messageMapsFromAny(message["tool_calls"]) {
				stored[stringFromAny(call["id"])] = call["_toolLabel"]
			}
		}
	}
	if stored["call-mcp"] != "datascope_query" || stored["call-read"] != nil {
		t.Fatalf("stored labels = %#v", stored)
	}

	detail, err := store.LoadChat(chatID)
	if err != nil {
		t.Fatal(err)
	}
	replayed := map[string]string{}
	for _, event := range detail.Events {
		if event.Type == "tool.snapshot" {
			replayed[event.String("toolId")] = event.String("toolLabel")
		}
	}
	if replayed["call-mcp"] != "datascope_query" || replayed["call-read"] != "" {
		t.Fatalf("replayed labels = %#v", replayed)
	}
}

func TestCompactAtomsIgnoreStoredToolLabel(t *testing.T) {
	call := func(stored bool) map[string]any {
		out := map[string]any{"id": "call", "type": "function", "function": map[string]any{"name": "mcp_a06a12877776ca4b_datascope_query", "arguments": "{}"}}
		if stored {
			out["_toolId"], out["_toolLabel"] = "call", "datascope_query"
		}
		return out
	}
	provider := compactMessageAtoms(map[string]any{"role": "assistant", "tool_calls": []any{call(false)}})
	stored := compactMessageAtoms(map[string]any{"role": "assistant", "tool_calls": []any{call(true)}})
	if len(provider) != 1 || len(stored) != 1 || provider[0].key != stored[0].key {
		t.Fatalf("provider=%#v stored=%#v", provider, stored)
	}
}
