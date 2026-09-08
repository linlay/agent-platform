package chat

import (
	"encoding/json"
	"testing"
)

func TestViewMetadataPersistsForReplayButNotModelContext(t *testing.T) {
	ref := map[string]any{"connectorId": "crm", "key": "card", "hash": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "renderer": "html"}
	stored := StoredMessage{Role: "tool", ToolCallID: "call", ToolID: "call", Content: textContent(`{"count":3}`), View: ref, ViewError: "view_unavailable"}
	data, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	var msg map[string]any
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatal(err)
	}
	msg["ts"] = int64(1700000000000)
	events, err := storedMessageToEventsWithOptions(msg, "run", "", "", 0, func() int64 { return 1 }, replayMessageOptions{})
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%#v error=%v", events, err)
	}
	if events[0].Type != "tool.result" || events[0].Payload["view"].(map[string]any)["hash"] != ref["hash"] || events[0].Payload["viewError"] != "view_unavailable" {
		t.Fatalf("lost presentation metadata: %#v", events)
	}
	line := map[string]any{"_type": StepLineTypeReactTool, "runId": "run", "messages": []any{msg}}
	for _, messages := range [][]map[string]any{rawMessagesFromJSONLLines([]map[string]any{line}), normalizedStepMessages(line, "run")} {
		if len(messages) != 1 {
			t.Fatalf("raw messages=%#v", messages)
		}
		if _, exists := messages[0]["view"]; exists {
			t.Fatal("presentation reference entered model context")
		}
		if _, exists := messages[0]["viewError"]; exists {
			t.Fatal("presentation error entered model context")
		}
		if messages[0]["content"] != `{"count":3}` {
			t.Fatalf("business result changed: %#v", messages[0])
		}
	}
}
