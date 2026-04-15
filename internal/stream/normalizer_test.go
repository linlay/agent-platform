package stream

import "testing"

func TestNormalizerDropsAwaitingAnswerForHiddenTool(t *testing.T) {
	normalizer := NewNormalizer()
	normalizer.RegisterHiddenTools("hidden_tool")

	events := []StreamEvent{
		NewEvent("tool.start", map[string]any{
			"toolId":   "tool_1",
			"toolName": "hidden_tool",
		}),
		NewEvent("awaiting.answer", map[string]any{
			"awaitingId": "tool_1",
			"mode":       "approval",
			"value":      "approve",
		}),
	}

	got := normalizer.Normalize(events)
	if len(got) != 0 {
		t.Fatalf("expected hidden awaiting.answer to be dropped, got %#v", got)
	}
}
