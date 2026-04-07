package stream

import (
	"errors"
	"testing"
)

func TestDispatcherClosesContentWhenSwitchingToTool(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	events := dispatcher.Dispatch(ContentDelta{ContentID: "run_1_c_1", Delta: "hello"})
	assertEventTypes(t, events, "content.start", "content.delta")

	events = dispatcher.Dispatch(ToolArgs{
		ToolID:     "tool_1",
		ToolName:   "_datetime_",
		ToolType:   "backend",
		Delta:      "{",
		ChunkIndex: 0,
	})
	assertEventTypes(t, events, "content.end", "tool.start", "tool.args")
}

func TestDispatcherEmitsToolSnapshotAndResultLifecycle(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	_ = dispatcher.Dispatch(ToolArgs{
		ToolID:     "tool_1",
		ToolName:   "_datetime_",
		ToolType:   "backend",
		Delta:      "{",
		ChunkIndex: 0,
	})
	endEvents := dispatcher.Dispatch(ToolEnd{ToolID: "tool_1"})
	assertEventTypes(t, endEvents, "tool.end", "tool.snapshot")

	resultEvents := dispatcher.Dispatch(ToolResult{
		ToolID:   "tool_1",
		ToolName: "_datetime_",
		Result:   map[string]any{"iso8601": "2026-01-01T00:00:00Z"},
	})
	assertEventTypes(t, resultEvents, "tool.result")
}

func TestDispatcherCompleteEmitsReasoningSnapshot(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	events := dispatcher.Dispatch(ReasoningDelta{ReasoningID: "run_1_r_1", Delta: "thinking..."})
	assertEventTypes(t, events, "reasoning.start", "reasoning.delta")

	events = dispatcher.Dispatch(ReasoningDelta{ReasoningID: "run_1_r_1", Delta: " more"})
	assertEventTypes(t, events, "reasoning.delta")

	events = dispatcher.Dispatch(ContentDelta{ContentID: "run_1_c_1", Delta: "hello"})
	assertEventTypes(t, events, "reasoning.end", "content.start", "content.delta")

	events = dispatcher.Dispatch(InputRunComplete{FinishReason: "stop"})
	assertEventTypes(t, events)

	completeEvents := dispatcher.Complete()
	var types []string
	for _, event := range completeEvents {
		types = append(types, event.Type)
	}

	found := false
	for _, event := range completeEvents {
		if event.Type == "reasoning.snapshot" {
			found = true
			data := event.ToData()
			if data["reasoningId"] != "run_1_r_1" {
				t.Fatalf("expected reasoningId=run_1_r_1, got %v", data["reasoningId"])
			}
			if data["text"] != "thinking... more" {
				t.Fatalf("expected full reasoning text, got %v", data["text"])
			}
			break
		}
	}
	if !found {
		t.Fatalf("expected reasoning.snapshot in complete events, got types: %v", types)
	}
}

func TestDispatcherFailClosesOpenBlocksAndEmitsRunError(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	_ = dispatcher.Dispatch(ContentDelta{ContentID: "run_1_c_1", Delta: "partial"})
	events := dispatcher.Fail(errors.New("boom"))
	assertEventTypes(t, events, "content.end", "run.error")

	last := events[len(events)-1].ToData()
	errPayload, _ := last["error"].(map[string]any)
	if errPayload["code"] != "stream_failed" {
		t.Fatalf("expected stream_failed code, got %#v", errPayload)
	}
}

func assertEventTypes(t *testing.T, events []StreamEvent, want ...string) {
	t.Helper()
	if len(events) != len(want) {
		t.Fatalf("expected %d events, got %d", len(want), len(events))
	}
	for idx, event := range events {
		if event.Type != want[idx] {
			t.Fatalf("event %d: expected %s, got %s", idx, want[idx], event.Type)
		}
	}
}
