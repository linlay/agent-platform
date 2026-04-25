package stream

import (
	"encoding/json"
	"errors"
	"reflect"
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
		ToolName:   "datetime",
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
		ToolName:   "datetime",
		Delta:      "{",
		ChunkIndex: 0,
	})
	endEvents := dispatcher.Dispatch(ToolEnd{ToolID: "tool_1"})
	assertEventTypes(t, endEvents, "tool.end", "tool.snapshot")

	resultEvents := dispatcher.Dispatch(ToolResult{
		ToolID:   "tool_1",
		ToolName: "datetime",
		Result:   map[string]any{"iso8601": "2026-01-01T00:00:00Z"},
	})
	assertEventTypes(t, resultEvents, "tool.result")
}

func TestDispatcherEmitsDedicatedMemoryEventAlongsideToolResult(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	events := dispatcher.Dispatch(ToolResult{
		ToolID:   "tool_mem_1",
		ToolName: "_memory_write_",
		Result: map[string]any{
			"status": "stored",
			"memory": map[string]any{"id": "mem_1"},
		},
	})
	assertEventTypes(t, events, "tool.result", "memory.write")
	payload := events[1].ToData()
	if payload["runId"] != "run_1" || payload["chatId"] != "chat_1" {
		t.Fatalf("unexpected memory.write envelope: %#v", payload)
	}
	data, _ := payload["data"].(map[string]any)
	if data["toolName"] != "_memory_write_" {
		t.Fatalf("unexpected memory.write toolName: %#v", data)
	}
}

func TestDispatcherFallsBackToActiveTaskIDForSubAgentBlocks(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	startEvents := dispatcher.Dispatch(TaskStart{
		TaskID:      "task_sub_1",
		RunID:       "run_1",
		TaskName:    "分析",
		SubAgentKey: "analyzer",
		MainToolID:  "tool_main_1",
	})
	assertEventTypes(t, startEvents, "task.start")

	contentEvents := dispatcher.Dispatch(ContentDelta{
		ContentID: "run_1_c_1",
		Delta:     "child output",
	})
	assertEventTypes(t, contentEvents, "content.start", "content.delta")
	if got := contentEvents[0].Data().String("taskId"); got != "task_sub_1" {
		t.Fatalf("expected content.start taskId fallback to active task, got %#v", contentEvents[0].ToData())
	}

	toolEvents := dispatcher.Dispatch(ToolArgs{
		ToolID:     "tool_sub_1",
		ToolName:   "datetime",
		Delta:      "{",
		ChunkIndex: 0,
	})
	assertEventTypes(t, toolEvents, "content.end", "content.snapshot", "tool.start", "tool.args")
	if got := toolEvents[2].Data().String("taskId"); got != "task_sub_1" {
		t.Fatalf("expected tool.start taskId fallback to active task, got %#v", toolEvents[2].ToData())
	}
}

func TestDispatcherEmitsApprovalAlongsideToolResult(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	events := dispatcher.Dispatch(ToolResult{
		ToolID:   "tool_1",
		ToolName: "bash",
		Result:   "",
		Hitl: map[string]any{
			"awaitingId": "await_1",
			"decision":   "approve",
			"ruleKey":    "dangerous-commands::chmod",
		},
	})
	assertEventTypes(t, events, "tool.result")
	payload := events[0].ToData()
	approval, ok := payload["approval"].(map[string]any)
	if !ok || approval["decision"] != "approve" || approval["awaitingId"] != "await_1" {
		t.Fatalf("expected approval payload on tool.result, got %#v", payload)
	}
	if _, ok := payload["hitl"]; ok {
		t.Fatalf("did not expect legacy hitl key, got %#v", payload)
	}
}

func TestDispatcherEmitsQuestionModeAwaitAskAfterToolStart(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	events := dispatcher.Dispatch(ToolArgs{
		ToolID:     "tool_1",
		ToolName:   "ask_user_question",
		Delta:      "{",
		ChunkIndex: 0,
		AwaitAsk: &AwaitAsk{
			AwaitingID:   "tool_1",
			ViewportType: "builtin",
			ViewportKey:  "confirm_dialog",
			Mode:         "question",
			Timeout:      120000,
			RunID:        "run_1",
		},
	})
	assertEventTypes(t, events, "tool.start", "awaiting.ask", "tool.args")
}

func TestDispatcherEmitsApprovalModeAwaitAskWithQuestions(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	viewportEvents := dispatcher.Dispatch(AwaitAsk{
		AwaitingID: "tool_1",
		Mode:       "approval",
		Timeout:    120000,
		RunID:      "run_1",
		Approvals: []any{
			map[string]any{
				"id":                  "cmd-1",
				"command":             "git push origin main",
				"description":         "推送主分支",
				"options":             []any{map[string]any{"label": "同意", "decision": "approve"}},
				"allowFreeText":       true,
				"freeTextPlaceholder": "可选：填写理由",
			},
		},
	})
	assertEventTypes(t, viewportEvents, "awaiting.ask")
	payload := viewportEvents[0].ToData()
	if _, exists := payload["viewportType"]; exists {
		t.Fatalf("did not expect viewport metadata on approval ask, got %#v", payload)
	}
	approvals, _ := payload["approvals"].([]any)
	if len(approvals) != 1 {
		t.Fatalf("expected approvals in approval awaiting.ask, got %#v", payload)
	}
}

func TestDispatcherSkipsDuplicateAwaitAskForSameAwaitingID(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	first := dispatcher.Dispatch(AwaitAsk{
		AwaitingID: "await_1",
		Mode:       "approval",
		Timeout:    120000,
		RunID:      "run_1",
		Approvals: []any{
			map[string]any{
				"id":                  "tool_1",
				"command":             "chmod 777 ~/a.sh",
				"description":         "放开 a.sh 权限",
				"options":             []any{map[string]any{"label": "同意", "decision": "approve"}},
				"allowFreeText":       true,
				"freeTextPlaceholder": "可选：填写理由",
			},
		},
	})
	assertEventTypes(t, first, "awaiting.ask")

	second := dispatcher.Dispatch(AwaitAsk{
		AwaitingID: "await_1",
		Mode:       "approval",
		Timeout:    120000,
		RunID:      "run_1",
		Approvals: []any{
			map[string]any{
				"id":                  "tool_1",
				"command":             "chmod 777 ~/a.sh",
				"description":         "放开 a.sh 权限",
				"options":             []any{map[string]any{"label": "同意", "decision": "approve"}},
				"allowFreeText":       true,
				"freeTextPlaceholder": "可选：填写理由",
			},
		},
	})
	if len(second) != 0 {
		t.Fatalf("expected duplicate awaiting.ask to be ignored, got %#v", second)
	}
}

func TestDispatcherEmitsApprovalModeAwaitAskWithPayloadOnlyForForm(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	events := dispatcher.Dispatch(AwaitAsk{
		AwaitingID:   "tool_1",
		ViewportType: "html",
		ViewportKey:  "leave_form",
		Mode:         "form",
		Timeout:      120000,
		RunID:        "run_1",
		Forms: []any{
			map[string]any{
				"id":    "form-1",
				"title": "mock 请假申请",
				"payload": map[string]any{
					"applicant":  "Lin",
					"days":       3,
					"leave_type": "年假",
				},
			},
		},
	})
	assertEventTypes(t, events, "awaiting.ask")
	payload := events[0].ToData()
	forms, _ := payload["forms"].([]any)
	if len(forms) != 1 {
		t.Fatalf("expected forms in form awaiting.ask, got %#v", payload)
	}
	form := forms[0].(map[string]any)
	if _, exists := form["command"]; exists {
		t.Fatalf("did not expect form command in awaiting.ask payload, got %#v", payload)
	}
	formPayload, _ := form["payload"].(map[string]any)
	applicant, _ := formPayload["applicant"].(string)
	if formPayload == nil || applicant != "Lin" || formPayload["days"] != 3 {
		t.Fatalf("expected payload in form awaiting.ask, got %#v", payload)
	}
	if form["title"] != "mock 请假申请" {
		t.Fatalf("expected title in form awaiting.ask, got %#v", payload)
	}
	if _, exists := payload["viewportPayload"]; exists {
		t.Fatalf("did not expect viewportPayload forms, got %#v", payload)
	}
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
		"runId":           "run_1",
		"toolName":        "datetime",
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
		`"toolName":"datetime"`,
		`"taskId":"task_1"`,
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

func TestEventDataMarshalsAwaitAskWithContractKeyOrder(t *testing.T) {
	event := NewEvent("awaiting.ask", map[string]any{
		"timeout":    120000,
		"runId":      "run_1",
		"mode":       "approval",
		"awaitingId": "tool_1",
	})
	event.Seq = 9
	data, err := json.Marshal(event.Data())
	if err != nil {
		t.Fatalf("marshal event data: %v", err)
	}
	text := string(data)
	order := []string{
		`"seq":9`,
		`"type":"awaiting.ask"`,
		`"awaitingId":"tool_1"`,
		`"mode":"approval"`,
		`"timeout":120000`,
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

func TestEventDataMarshalsAwaitAskWithFormsBeforeTimestamp(t *testing.T) {
	event := NewEvent("awaiting.ask", map[string]any{
		"awaitingId":   "tool_1",
		"viewportType": "html",
		"viewportKey":  "leave_form",
		"mode":         "form",
		"timeout":      120000,
		"runId":        "run_1",
		"forms": []any{
			map[string]any{
				"id":    "form-1",
				"title": "mock 请假申请",
				"payload": map[string]any{
					"applicant": "Lin",
				},
			},
		},
	})
	data, err := json.Marshal(event.Data())
	if err != nil {
		t.Fatalf("marshal event data: %v", err)
	}
	text := string(data)
	formsIndex := strings.Index(text, `"forms":[{"id":"form-1","payload":{"applicant":"Lin"},"title":"mock 请假申请"}]`)
	timestampIndex := strings.Index(text, `"timestamp":`)
	if formsIndex < 0 || timestampIndex < 0 || formsIndex >= timestampIndex {
		t.Fatalf("expected forms before timestamp in %s", text)
	}
	if strings.Contains(text, `"viewportPayload":`) {
		t.Fatalf("did not expect viewportPayload in %s", text)
	}
}

func TestEventDataMarshalsApprovalAwaitAskWithQuestions(t *testing.T) {
	event := NewEvent("awaiting.ask", map[string]any{
		"awaitingId": "tool_1",
		"mode":       "approval",
		"timeout":    120000,
		"runId":      "run_1",
		"approvals": []any{
			map[string]any{"id": "cmd-1", "command": "Proceed?"},
		},
	})
	data, err := json.Marshal(event.Data())
	if err != nil {
		t.Fatalf("marshal event data: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `"approvals":[`) {
		t.Fatalf("expected approvals in approval awaiting.ask: %s", text)
	}
}

func TestEventDataMarshalsRequestSubmitWithoutViewID(t *testing.T) {
	event := NewEvent("request.submit", map[string]any{
		"requestId":  "req_1",
		"chatId":     "chat_1",
		"runId":      "run_1",
		"awaitingId": "tool_1",
		"params": []any{
			map[string]any{"id": "cmd-1", "decision": "approve"},
		},
	})
	event.Seq = 11
	data, err := json.Marshal(event.Data())
	if err != nil {
		t.Fatalf("marshal event data: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `"params":[{"decision":"approve","id":"cmd-1"}]`) {
		t.Fatalf("expected params in request.submit payload: %s", text)
	}
	if strings.Contains(text, `"viewId"`) {
		t.Fatalf("did not expect viewId in request.submit payload: %s", text)
	}
}

func TestDispatcherEmitsAwaitingAnswerForApprovalMode(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	events := dispatcher.Dispatch(AwaitingAnswer{
		AwaitingID: "tool_1",
		Answer: map[string]any{
			"mode":   "approval",
			"status": "answered",
			"approvals": []any{
				map[string]any{
					"id":       "cmd-1",
					"command":  "Proceed?",
					"decision": "approve",
				},
			},
		},
	})
	assertEventTypes(t, events, "awaiting.answer")
	payload := events[0].ToData()
	if payload["mode"] != "approval" {
		t.Fatalf("expected approval mode, got %#v", payload)
	}
	if payload["status"] != "answered" {
		t.Fatalf("expected answered status, got %#v", payload)
	}
	approvals, _ := payload["approvals"].([]map[string]any)
	if len(approvals) != 1 {
		t.Fatalf("expected formatted approvals, got %#v", payload)
	}
	if approvals[0]["id"] != "cmd-1" || approvals[0]["command"] != "Proceed?" || approvals[0]["decision"] != "approve" {
		t.Fatalf("unexpected approval awaiting.answer payload %#v", approvals[0])
	}
}

func TestDispatcherEmitsAwaitingAnswerForApprovalFormSubmit(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	events := dispatcher.Dispatch(AwaitingAnswer{
		AwaitingID: "tool_1",
		Answer: map[string]any{
			"mode":   "form",
			"status": "answered",
			"forms": []any{
				map[string]any{
					"id":     "form-1",
					"action": "submit",
					"payload": map[string]any{
						"applicant_id":  "E1001",
						"department_id": "engineering",
						"days":          2,
					},
				},
			},
		},
	})
	assertEventTypes(t, events, "awaiting.answer")
	payload := events[0].ToData()
	if payload["mode"] != "form" {
		t.Fatalf("unexpected form awaiting.answer payload %#v", payload)
	}
	if payload["status"] != "answered" {
		t.Fatalf("expected answered status, got %#v", payload)
	}
	forms, _ := payload["forms"].([]map[string]any)
	if len(forms) != 1 {
		t.Fatalf("expected one form answer, got %#v", payload)
	}
	formPayload, _ := forms[0]["payload"].(map[string]any)
	if forms[0]["action"] != "submit" || formPayload["applicant_id"] != "E1001" || formPayload["days"] != 2 {
		t.Fatalf("unexpected approval form payload %#v", payload)
	}
}

func TestDispatcherEmitsAwaitingAnswerForQuestionMode(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	events := dispatcher.Dispatch(AwaitingAnswer{
		AwaitingID: "tool_1",
		Answer: map[string]any{
			"mode":   "question",
			"status": "answered",
			"answers": []any{
				map[string]any{
					"id":       "q1",
					"question": "Destination?",
					"header":   "Trip",
					"answer":   []string{"Xitang", "Suzhou"},
				},
				map[string]any{
					"id":       "q2",
					"question": "How many people?",
					"answer":   2,
				},
			},
		},
	})
	assertEventTypes(t, events, "awaiting.answer")
	payload := events[0].ToData()
	if payload["mode"] != "question" {
		t.Fatalf("expected question mode, got %#v", payload)
	}
	if payload["status"] != "answered" {
		t.Fatalf("expected answered status, got %#v", payload)
	}
	answers, _ := payload["answers"].([]map[string]any)
	if len(answers) != 2 {
		t.Fatalf("expected formatted answers, got %#v", payload)
	}
	firstAnswers, _ := answers[0]["answers"].([]string)
	if answers[0]["id"] != "q1" || answers[0]["question"] != "Destination?" || answers[0]["header"] != "Trip" || !reflect.DeepEqual(firstAnswers, []string{"Xitang", "Suzhou"}) {
		t.Fatalf("unexpected formatted answers %#v", answers)
	}
	if answers[1]["id"] != "q2" || answers[1]["question"] != "How many people?" || answers[1]["answer"] != 2 {
		t.Fatalf("unexpected scalar formatted answer %#v", answers[1])
	}
}

func TestDispatcherEmitsAwaitingAnswerErrorFields(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	events := dispatcher.Dispatch(AwaitingAnswer{
		AwaitingID: "tool_1",
		Answer: map[string]any{
			"mode":   "question",
			"status": "error",
			"error": map[string]any{
				"code":    "user_dismissed",
				"message": "用户关闭等待项",
			},
		},
	})
	assertEventTypes(t, events, "awaiting.answer")
	payload := events[0].ToData()
	if payload["mode"] != "question" || payload["status"] != "error" {
		t.Fatalf("unexpected error awaiting.answer payload %#v", payload)
	}
	errPayload, _ := payload["error"].(map[string]any)
	if errPayload["code"] != "user_dismissed" || errPayload["message"] != "用户关闭等待项" {
		t.Fatalf("unexpected error payload %#v", payload)
	}
	if _, exists := payload["answers"]; exists {
		t.Fatalf("did not expect answers on error awaiting.answer, got %#v", payload)
	}
}

func TestEventDataMarshalsAwaitingAnswerWithContractKeyOrder(t *testing.T) {
	event := NewEvent("awaiting.answer", map[string]any{
		"awaitingId": "tool_1",
		"mode":       "question",
		"status":     "error",
		"error": map[string]any{
			"code":    "user_dismissed",
			"message": "用户关闭等待项",
		},
	})
	event.Seq = 12
	data, err := json.Marshal(event.Data())
	if err != nil {
		t.Fatalf("marshal event data: %v", err)
	}
	text := string(data)
	order := []string{
		`"seq":12`,
		`"type":"awaiting.answer"`,
		`"awaitingId":"tool_1"`,
		`"mode":"question"`,
		`"status":"error"`,
		`"error":{"code":"user_dismissed","message":"用户关闭等待项"}`,
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

func TestEventDataMarshalsAwaitingAnswerFormSubmitWithContractKeyOrder(t *testing.T) {
	event := NewEvent("awaiting.answer", map[string]any{
		"awaitingId": "tool_1",
		"mode":       "form",
		"status":     "answered",
		"forms": []any{
			map[string]any{
				"id":     "form-1",
				"action": "submit",
				"payload": map[string]any{
					"applicant_id": "E1001",
				},
			},
		},
	})
	event.Seq = 13
	data, err := json.Marshal(event.Data())
	if err != nil {
		t.Fatalf("marshal event data: %v", err)
	}
	text := string(data)
	order := []string{
		`"seq":13`,
		`"type":"awaiting.answer"`,
		`"awaitingId":"tool_1"`,
		`"mode":"form"`,
		`"status":"answered"`,
		`"forms":[{"action":"submit","id":"form-1","payload":{"applicant_id":"E1001"}}]`,
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
