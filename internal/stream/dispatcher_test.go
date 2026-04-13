package stream

import (
	"encoding/json"
	"errors"
	"strings"
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
	assertEventTypes(t, events, "content.end", "content.snapshot", "tool.start", "tool.args")
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

func TestDispatcherEmitsQuestionModeAwaitQuestionAfterToolStart(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	events := dispatcher.Dispatch(ToolArgs{
		ToolID:     "tool_1",
		ToolName:   "_ask_user_question_",
		ToolType:   "builtin",
		Delta:      "{",
		ChunkIndex: 0,
		AwaitQuestion: &AwaitQuestion{
			AwaitID:      "tool_1",
			ViewportType: "builtin",
			ViewportKey:  "confirm_dialog",
			Mode:         "question",
			ToolTimeout:  120000,
			RunID:        "run_1",
		},
	})
	assertEventTypes(t, events, "tool.start", "await.question", "tool.args")
}

func TestDispatcherEmitsApprovalModeAwaitQuestionWithQuestions(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	viewportEvents := dispatcher.Dispatch(AwaitQuestion{
		AwaitID:      "tool_1",
		ViewportType: "builtin",
		ViewportKey:  "confirm_dialog",
		Mode:         "approval",
		ToolTimeout:  120000,
		RunID:        "run_1",
		Questions: []any{
			map[string]any{"question": "Proceed?", "options": []any{map[string]any{"label": "Yes", "value": "yes"}}},
		},
	})
	assertEventTypes(t, viewportEvents, "await.question")

	payloadEvents := dispatcher.Dispatch(AwaitPayload{
		AwaitID:   "tool_1",
		Questions: []any{map[string]any{"question": "How many?", "type": "number"}},
	})
	assertEventTypes(t, payloadEvents, "await.payload")
}

func TestDispatcherCompleteEmitsReasoningSnapshot(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	events := dispatcher.Dispatch(ReasoningDelta{
		ReasoningID:    "run_1_r_1",
		ReasoningLabel: "reasoning_details",
		Delta:          "thinking...",
	})
	assertEventTypes(t, events, "reasoning.start", "reasoning.delta")
	start := events[0].ToData()
	startLabel := start["reasoningLabel"]
	if startLabel == "" {
		t.Fatalf("expected reasoning.start to include reasoningLabel, got %#v", start)
	}
	if startLabel == "reasoning_details" {
		t.Fatalf("expected reasoning.start to use display phrase instead of internal source tag, got %#v", start)
	}
	if startLabel != ReasoningLabelForID("run_1_r_1") {
		t.Fatalf("expected reasoning.start to use deterministic display phrase, got %#v", start)
	}

	events = dispatcher.Dispatch(ReasoningDelta{
		ReasoningID:    "run_1_r_1",
		ReasoningLabel: "thinking_delta",
		Delta:          " more",
	})
	assertEventTypes(t, events, "reasoning.delta")

	events = dispatcher.Dispatch(ContentDelta{ContentID: "run_1_c_1", Delta: "hello"})
	assertEventTypes(t, events, "reasoning.end", "reasoning.snapshot", "content.start", "content.delta")
	snapshot := events[1].ToData()
	if snapshot["reasoningLabel"] != startLabel {
		t.Fatalf("expected reasoning.snapshot to reuse reasoningLabel %q, got %#v", startLabel, snapshot)
	}

	events = dispatcher.Dispatch(InputRunComplete{FinishReason: "stop"})
	assertEventTypes(t, events)

	completeEvents := dispatcher.Complete()
	assertEventTypes(t, completeEvents, "content.end", "content.snapshot", "run.complete")
}

func TestDispatcherNeverEmitsInternalReasoningSourceAsReasoningLabel(t *testing.T) {
	internalLabels := []string{
		"reasoning_details",
		"reasoning_content",
		"thinking_delta",
		"think_tag",
	}

	for _, internalLabel := range internalLabels {
		dispatcher := NewDispatcher(StreamRequest{
			RunID:  "run_1",
			ChatID: "chat_1",
		})

		events := dispatcher.Dispatch(ReasoningDelta{
			ReasoningID:    "run_1_r_9",
			ReasoningLabel: internalLabel,
			Delta:          "thinking...",
		})
		assertEventTypes(t, events, "reasoning.start", "reasoning.delta")

		start := events[0].ToData()
		if start["reasoningLabel"] == internalLabel {
			t.Fatalf("expected reasoning.start not to expose internal reasoning label %q, got %#v", internalLabel, start)
		}
	}
}

func TestEventDataMarshalsReasoningSnapshotWithContractKeyOrder(t *testing.T) {
	event := NewEvent("reasoning.snapshot", map[string]any{
		"reasoningId":    "reasoning_1",
		"runId":          "run_1",
		"text":           "thinking",
		"taskId":         "task_1",
		"reasoningLabel": "正在思考",
	})
	event.Seq = 8
	data, err := json.Marshal(event.Data())
	if err != nil {
		t.Fatalf("marshal event data: %v", err)
	}
	text := string(data)
	order := []string{
		`"seq":8`,
		`"type":"reasoning.snapshot"`,
		`"reasoningId":"reasoning_1"`,
		`"runId":"run_1"`,
		`"text":"thinking"`,
		`"taskId":"task_1"`,
		`"reasoningLabel":"正在思考"`,
		`"timestamp":`,
	}
	prev := -1
	for _, part := range order {
		idx := strings.Index(text, part)
		if idx < 0 {
			t.Fatalf("expected %q in %s", part, text)
		}
		if idx <= prev {
			t.Fatalf("expected ordered keys in %s", text)
		}
		prev = idx
	}
}

func TestDispatcherEmitsActionSnapshotAndResultLifecycle(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	_ = dispatcher.Dispatch(ActionArgs{
		ActionID:    "action_1",
		ActionName:  "approval_action",
		Description: "Need confirmation",
		Delta:       `{"confirmed":`,
	})
	endEvents := dispatcher.Dispatch(ActionEnd{ActionID: "action_1"})
	assertEventTypes(t, endEvents, "action.end", "action.snapshot")

	resultEvents := dispatcher.Dispatch(ActionResult{
		ActionID: "action_1",
		Result:   map[string]any{"confirmed": true},
	})
	assertEventTypes(t, resultEvents, "action.result")
}

func TestDispatcherFailClosesOpenBlocksAndEmitsRunError(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	_ = dispatcher.Dispatch(ContentDelta{ContentID: "run_1_c_1", Delta: "partial"})
	events := dispatcher.Fail(errors.New("boom"))
	assertEventTypes(t, events, "content.end", "content.snapshot", "run.error")

	last := events[len(events)-1].ToData()
	errPayload, _ := last["error"].(map[string]any)
	if errPayload["code"] != "stream_failed" {
		t.Fatalf("expected stream_failed code, got %#v", errPayload)
	}
}

func TestEventDataMarshalsWithContractKeyOrder(t *testing.T) {
	event := NewEvent("tool.snapshot", map[string]any{
		"arguments":       "{}",
		"toolDescription": "desc",
		"taskId":          "task_1",
		"toolLabel":       "Datetime",
		"toolType":        "function",
		"runId":           "run_1",
		"toolName":        "_datetime_",
		"toolId":          "tool_1",
	})
	event.Seq = 7
	data, err := json.Marshal(event.Data())
	if err != nil {
		t.Fatalf("marshal event data: %v", err)
	}
	text := string(data)
	order := []string{
		`"seq":7`,
		`"type":"tool.snapshot"`,
		`"toolId":"tool_1"`,
		`"runId":"run_1"`,
		`"toolName":"_datetime_"`,
		`"taskId":"task_1"`,
		`"toolType":"function"`,
		`"toolLabel":"Datetime"`,
		`"toolDescription":"desc"`,
		`"arguments":"{}"`,
		`"timestamp":`,
	}
	prev := -1
	for _, part := range order {
		idx := strings.Index(text, part)
		if idx < 0 {
			t.Fatalf("expected %q in %s", part, text)
		}
		if idx <= prev {
			t.Fatalf("expected ordered keys in %s", text)
		}
		prev = idx
	}
}

func TestEventDataMarshalsAwaitQuestionWithContractKeyOrder(t *testing.T) {
	event := NewEvent("await.question", map[string]any{
		"toolTimeout":  120000,
		"runId":        "run_1",
		"viewportKey":  "confirm_dialog",
		"mode":         "approval",
		"awaitId":      "tool_1",
		"viewportType": "builtin",
	})
	event.Seq = 9
	data, err := json.Marshal(event.Data())
	if err != nil {
		t.Fatalf("marshal event data: %v", err)
	}
	text := string(data)
	order := []string{
		`"seq":9`,
		`"type":"await.question"`,
		`"awaitId":"tool_1"`,
		`"viewportType":"builtin"`,
		`"viewportKey":"confirm_dialog"`,
		`"mode":"approval"`,
		`"toolTimeout":120000`,
		`"runId":"run_1"`,
		`"timestamp":`,
	}
	prev := -1
	for _, part := range order {
		idx := strings.Index(text, part)
		if idx < 0 {
			t.Fatalf("expected %q in %s", part, text)
		}
		if idx <= prev {
			t.Fatalf("expected ordered keys in %s", text)
		}
		prev = idx
	}
}

func TestEventDataMarshalsApprovalAwaitQuestionWithQuestions(t *testing.T) {
	event := NewEvent("await.question", map[string]any{
		"awaitId":      "tool_1",
		"viewportType": "builtin",
		"viewportKey":  "confirm_dialog",
		"mode":         "approval",
		"toolTimeout":  120000,
		"runId":        "run_1",
		"questions": []any{
			map[string]any{"question": "Proceed?"},
		},
	})
	data, err := json.Marshal(event.Data())
	if err != nil {
		t.Fatalf("marshal event data: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `"questions":[`) {
		t.Fatalf("expected questions in approval await.question: %s", text)
	}
}

func TestEventDataMarshalsAwaitPayloadWithQuestions(t *testing.T) {
	event := NewEvent("await.payload", map[string]any{
		"awaitId": "tool_1",
		"questions": []any{
			map[string]any{
				"question": "Destination?",
				"type":     "select",
			},
		},
	})
	event.Seq = 10
	data, err := json.Marshal(event.Data())
	if err != nil {
		t.Fatalf("marshal event data: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `"questions":[`) {
		t.Fatalf("expected top-level questions in await.payload: %s", text)
	}
	if strings.Contains(text, `"payload":`) {
		t.Fatalf("did not expect payload wrapper in await.payload: %s", text)
	}
}

func TestEventDataMarshalsAwaitAnswerWithoutViewID(t *testing.T) {
	event := NewEvent("await.answer", map[string]any{
		"requestId": "req_1",
		"chatId":    "chat_1",
		"runId":     "run_1",
		"toolId":    "tool_1",
		"payload":   map[string]any{"value": "approve"},
	})
	event.Seq = 11
	data, err := json.Marshal(event.Data())
	if err != nil {
		t.Fatalf("marshal event data: %v", err)
	}
	text := string(data)
	if strings.Contains(text, `"viewId"`) {
		t.Fatalf("did not expect viewId in await.answer payload: %s", text)
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
