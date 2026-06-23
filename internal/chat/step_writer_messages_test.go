package chat

import "testing"

func TestCanonicalizeStoredToolResultOrderUsesAssistantToolCallOrder(t *testing.T) {
	messages := []StoredMessage{
		{
			Role:  "assistant",
			MsgID: "msg_1",
			ToolCalls: []StoredToolCall{
				{ID: "tool_1", Type: "function", Function: StoredFunction{Name: "bash"}},
				{ID: "tool_2", Type: "function", Function: StoredFunction{Name: "bash"}},
				{ID: "tool_3", Type: "function", Function: StoredFunction{Name: "bash"}},
			},
		},
		{Role: "tool", ToolCallID: "tool_3", ToolID: "tool_3", Content: textContent("three")},
		{Role: "tool", ToolCallID: "tool_1", ToolID: "tool_1", Content: textContent("one")},
		{Role: "tool", ToolCallID: "tool_2", ToolID: "tool_2", Content: textContent("two")},
	}

	got := canonicalizeStoredToolResultOrder(messages)
	for index, wantID := range []string{"tool_1", "tool_2", "tool_3"} {
		message := got[index+1]
		if message.Role != "tool" || message.ToolCallID != wantID {
			t.Fatalf("expected tool result %s at index %d, got %#v", wantID, index+1, message)
		}
	}
}
