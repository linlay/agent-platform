package chat

import (
	"encoding/json"
	"testing"
)

func TestToolContentModelOutputPreservesEmptyTextAndLegacyHistory(t *testing.T) {
	for _, projected := range []*string{nil, pointerToToolOutput("")} {
		stored := StoredMessage{Role: "tool", ToolCallID: "call", Content: textContent(`{"business":true}`), ModelOutput: projected}
		encoded, err := json.Marshal(stored)
		if err != nil {
			t.Fatal(err)
		}
		var message map[string]any
		if err := json.Unmarshal(encoded, &message); err != nil {
			t.Fatal(err)
		}
		message["ts"] = int64(1700000000000)
		line := map[string]any{"_type": StepLineTypeReactTool, "runId": "run", "messages": []any{message}}
		want := `{"business":true}`
		if projected != nil {
			want = ""
		}
		for _, messages := range [][]map[string]any{
			rawMessagesFromJSONLLines([]map[string]any{line}),
			normalizedStepMessages(line, "run"),
		} {
			if len(messages) != 1 || messages[0]["content"] != want {
				t.Fatalf("legacy or explicit empty model text changed: %#v", messages)
			}
			if _, exists := messages[0]["_modelOutput"]; exists {
				t.Fatal("internal model-output field entered provider message")
			}
		}
	}
}

func pointerToToolOutput(text string) *string { return &text }
