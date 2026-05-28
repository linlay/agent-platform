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
		ToolName: "memory_write",
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
	if data["toolName"] != "memory_write" {
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
	if got := startEvents[0].Data().String("invokingToolId"); got != "tool_main_1" {
		t.Fatalf("expected task.start invokingToolId, got %#v", startEvents[0].ToData())
	}
	if _, exists := startEvents[0].ToData()["toolId"]; exists {
		t.Fatalf("did not expect task.start toolId, got %#v", startEvents[0].ToData())
	}

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

func TestDispatcherClosesContentBeforeTaskComplete(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})
	dispatcher.Dispatch(TaskStart{
		TaskID:      "task_sub_1",
		RunID:       "run_1",
		TaskName:    "分析",
		SubAgentKey: "analyzer",
		MainToolID:  "tool_main_1",
	})
	dispatcher.Dispatch(ContentDelta{
		ContentID: "task_sub_1:final",
		TaskID:    "task_sub_1",
		Delta:     "马到成功",
	})

	events := dispatcher.Dispatch(TaskComplete{TaskID: "task_sub_1"})
	assertEventTypes(t, events, "content.end", "content.snapshot", "task.complete")
	if got := events[1].Data().String("taskId"); got != "task_sub_1" {
		t.Fatalf("expected content.snapshot taskId, got %#v", events[1].ToData())
	}
	if got := events[1].Data().String("text"); got != "马到成功" {
		t.Fatalf("expected content.snapshot text, got %#v", events[1].ToData())
	}
}

func TestDispatcherTaskTerminalPayloads(t *testing.T) {
	t.Run("complete", func(t *testing.T) {
		dispatcher := NewDispatcher(StreamRequest{RunID: "run_1", ChatID: "chat_1"})
		events := dispatcher.Dispatch(TaskComplete{TaskID: "task_1"})
		assertEventTypes(t, events, "task.complete")
		payload := events[0].Data().Payload
		if len(payload) != 1 || payload["taskId"] != "task_1" {
			t.Fatalf("unexpected task.complete payload %#v", payload)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		dispatcher := NewDispatcher(StreamRequest{RunID: "run_1", ChatID: "chat_1"})
		events := dispatcher.Dispatch(TaskCancel{TaskID: "task_1", Reason: "user_cancelled"})
		assertEventTypes(t, events, "task.cancel")
		payload := events[0].Data().Payload
		if len(payload) != 2 || payload["taskId"] != "task_1" || payload["reason"] != "user_cancelled" {
			t.Fatalf("unexpected task.cancel payload %#v", payload)
		}
	})

	t.Run("error", func(t *testing.T) {
		dispatcher := NewDispatcher(StreamRequest{RunID: "run_1", ChatID: "chat_1"})
		events := dispatcher.Dispatch(TaskError{
			TaskID: "task_1",
			Error:  map[string]any{"code": "boom", "message": "failed"},
		})
		assertEventTypes(t, events, "task.error")
		payload := events[0].Data().Payload
		if len(payload) != 2 || payload["taskId"] != "task_1" {
			t.Fatalf("unexpected task.error payload %#v", payload)
		}
		errPayload, _ := payload["error"].(map[string]any)
		if errPayload["code"] != "boom" || errPayload["message"] != "failed" {
			t.Fatalf("unexpected task.error error payload %#v", payload)
		}
	})
}

func TestDispatcherEmitsPlanningLifecycle(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RequestID: "req_1",
		RunID:     "run_1",
		ChatID:    "chat_1",
		AgentKey:  "coder",
	})

	start := dispatcher.Dispatch(PlanningStart{
		PlanningID:   "plan-run_1",
		PlanningFile: "/tmp/plan-run_1.md",
		Title:        "Plan",
		Status:       "started",
	})
	assertEventTypes(t, start, "planning.start")
	if got := start[0].Data().String("planningId"); got != "plan-run_1" {
		t.Fatalf("expected planningId, got %#v", start[0].ToData())
	}
	startData := start[0].Data()
	if got := startData.String("planningFile"); got != "plan-run_1.md" {
		t.Fatalf("expected planningFile basename, got %#v", start[0].ToData())
	}
	for _, key := range []string{"requestId", "agentKey", "status"} {
		if got := startData.Value(key); got != nil {
			t.Fatalf("did not expect planning.start %s, got %#v", key, start[0].ToData())
		}
	}

	delta := dispatcher.Dispatch(PlanningDelta{
		PlanningID: "plan-run_1",
		Delta:      "\n\n# Plan",
	})
	assertEventTypes(t, delta, "planning.delta")
	deltaData := delta[0].Data()
	if got := deltaData.Payload; len(got) != 2 || got["planningId"] != "plan-run_1" || got["delta"] != "\n\n# Plan" {
		t.Fatalf("unexpected planning.delta payload %#v", deltaData.Map())
	}

	snapshot := dispatcher.Dispatch(PlanningSnapshot{
		PlanningID:   "plan-run_1",
		PlanningFile: "/tmp/plan-run_1.md",
		Markdown:     "# Plan",
	})
	assertEventTypes(t, snapshot, "planning.snapshot")
	if got := snapshot[0].Data().String("planningFile"); got != "plan-run_1.md" {
		t.Fatalf("expected snapshot planningFile basename, got %#v", snapshot[0].ToData())
	}
	snapshotData := snapshot[0].Data()
	if got := snapshotData.String("text"); got != "# Plan" {
		t.Fatalf("expected snapshot text, got %#v", snapshot[0].ToData())
	}
	if got := snapshotData.Value("markdown"); got != nil {
		t.Fatalf("did not expect public planning.snapshot markdown, got %#v", snapshot[0].ToData())
	}

	end := dispatcher.Dispatch(PlanningEnd{
		PlanningID: "plan-run_1",
		Status:     "ready",
		Markdown:   "# Plan",
	})
	assertEventTypes(t, end, "planning.end")
	if got := end[0].Data().Payload; len(got) != 1 || got["planningId"] != "plan-run_1" {
		t.Fatalf("unexpected planning.end payload %#v", end[0].ToData())
	}
}

func TestDispatcherIncludesTaskIDOnDebugEvents(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	preEvents := dispatcher.Dispatch(InputDebugPreCall{
		TaskID:   "task_sub_1",
		ChatID:   "chat_1",
		ModelKey: "mock",
	})
	assertEventTypes(t, preEvents, "debug.preCall")
	if got := preEvents[0].Data().String("taskId"); got != "task_sub_1" {
		t.Fatalf("expected debug.preCall taskId, got %#v", preEvents[0].ToData())
	}

	postEvents := dispatcher.Dispatch(InputDebugPostCall{
		TaskID:                          "task_sub_1",
		ChatID:                          "chat_1",
		ModelKey:                        "mock",
		LLMReturnPromptTokens:           100,
		LLMReturnCompletionTokens:       50,
		LLMReturnTotalTokens:            150,
		LLMReturnCachedTokens:           64,
		LLMReturnReasoningTokens:        12,
		LLMReturnPromptCacheHitTokens:   64,
		LLMReturnPromptCacheMissTokens:  36,
		RunPromptTokens:                 100,
		RunCompletionTokens:             50,
		RunTotalTokens:                  150,
		RunCachedTokens:                 64,
		RunReasoningTokens:              12,
		RunPromptCacheHitTokens:         64,
		RunPromptCacheMissTokens:        36,
		LLMReturnLLMChatCompletionCount: 1,
		RunLLMChatCompletionCount:       1,
	})
	assertEventTypes(t, postEvents, "debug.postCall")
	if got := postEvents[0].Data().String("taskId"); got != "task_sub_1" {
		t.Fatalf("expected debug.postCall taskId, got %#v", postEvents[0].ToData())
	}
	postData, _ := postEvents[0].Data().Value("data").(map[string]any)
	usage, _ := postData["usage"].(map[string]any)
	llmUsage, _ := usage["llmReturnUsage"].(map[string]any)
	promptDetails, _ := llmUsage["promptTokensDetails"].(map[string]any)
	completionDetails, _ := llmUsage["completionTokensDetails"].(map[string]any)
	if promptDetails["cacheHitTokens"] != 64 || promptDetails["cacheMissTokens"] != 36 ||
		completionDetails["reasoningTokens"] != 12 ||
		llmUsage["llmChatCompletionCount"] != 1 {
		t.Fatalf("expected detailed llm usage, got %#v", usage)
	}
}

func TestDispatcherTerminalUsageIncludesLLMChatCompletionCountWithoutTokens(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	dispatcher.Dispatch(InputDebugPreCall{
		ChatID:                    "chat_1",
		ModelKey:                  "mock",
		RunLLMChatCompletionCount: 1,
	})
	dispatcher.Dispatch(InputRunComplete{FinishReason: "stop"})
	events := dispatcher.Complete()
	assertEventTypes(t, events, "run.complete")

	usage, _ := events[0].Data().Value("usage").(map[string]any)
	if usage == nil || usage["llmChatCompletionCount"] != 1 {
		t.Fatalf("expected terminal usage with llmChatCompletionCount, got %#v", events[0].ToData())
	}
}

func TestDispatcherUsageSnapshotIncludesTaskAndDeepSeekCacheUsage(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	events := dispatcher.Dispatch(InputUsageSnapshot{
		TaskID:                          "task_sub_1",
		ChatID:                          "chat_1",
		ModelKey:                        "deepseek-v4-pro",
		ContextWindow:                   128000,
		CurrentContextSize:              100,
		EstimatedNextCallSize:           200,
		LLMReturnPromptTokens:           100,
		LLMReturnCompletionTokens:       50,
		LLMReturnTotalTokens:            150,
		LLMReturnCachedTokens:           64,
		LLMReturnReasoningTokens:        12,
		LLMReturnPromptCacheHitTokens:   64,
		LLMReturnPromptCacheMissTokens:  36,
		LLMReturnLLMChatCompletionCount: 1,
		RunPromptTokens:                 300,
		RunCompletionTokens:             75,
		RunTotalTokens:                  375,
		RunCachedTokens:                 128,
		RunReasoningTokens:              24,
		RunPromptCacheHitTokens:         128,
		RunPromptCacheMissTokens:        172,
		RunLLMChatCompletionCount:       2,
	})
	assertEventTypes(t, events, "usage.snapshot")
	data := events[0].Data()
	if got := data.String("taskId"); got != "task_sub_1" {
		t.Fatalf("expected usage.snapshot taskId, got %#v", data.Map())
	}
	usage, _ := data.Value("usage").(map[string]any)
	current, _ := usage["current"].(map[string]any)
	run, _ := usage["run"].(map[string]any)
	currentPromptDetails, _ := current["promptTokensDetails"].(map[string]any)
	currentCompletionDetails, _ := current["completionTokensDetails"].(map[string]any)
	if currentPromptDetails["cacheHitTokens"] != 64 || currentPromptDetails["cacheMissTokens"] != 36 ||
		currentCompletionDetails["reasoningTokens"] != 12 {
		t.Fatalf("expected detailed current usage, got %#v", usage)
	}
	if _, exists := current["llmChatCompletionCount"]; exists {
		t.Fatalf("did not expect current llmChatCompletionCount, got %#v", usage)
	}
	runPromptDetails, _ := run["promptTokensDetails"].(map[string]any)
	if runPromptDetails["cacheHitTokens"] != 128 || runPromptDetails["cacheMissTokens"] != 172 || run["llmChatCompletionCount"] != 2 {
		t.Fatalf("expected detailed run usage, got %#v", usage)
	}
	cw, _ := data.Value("contextWindow").(map[string]any)
	if cw["maxSize"] != 128000 || cw["currentSize"] != 100 || cw["estimatedNextCallSize"] != 200 {
		t.Fatalf("unexpected context window %#v", cw)
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

func TestDispatcherEmitsQuestionModeAwaitAskAfterToolEnd(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:    "run_1",
		ChatID:   "chat_1",
		AgentKey: "agent_1",
	})

	events := dispatcher.Dispatch(ToolArgs{
		ToolID:     "tool_1",
		ToolName:   "ask_user_question",
		Delta:      "{",
		ChunkIndex: 0,
	})
	assertEventTypes(t, events, "tool.start", "tool.args")

	endEvents := dispatcher.Dispatch(ToolEnd{ToolID: "tool_1"})
	assertEventTypes(t, endEvents, "tool.end", "tool.snapshot")

	awaitEvents := dispatcher.Dispatch(AwaitAsk{
		AwaitingID:   "tool_1",
		ViewportType: "builtin",
		ViewportKey:  "confirm_dialog",
		Mode:         "question",
		Timeout:      120000,
		RunID:        "run_1",
	})
	assertEventTypes(t, awaitEvents, "awaiting.ask")
	payload := awaitEvents[0].ToData()
	if payload["agentKey"] != "agent_1" {
		t.Fatalf("expected agentKey on question awaiting.ask, got %#v", payload)
	}
}

func TestDispatcherEmitsApprovalModeAwaitAskWithQuestions(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:    "run_1",
		ChatID:   "chat_1",
		AgentKey: "agent_1",
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
	if payload["viewportType"] != "builtin" || payload["viewportKey"] != "approval" {
		t.Fatalf("expected builtin approval viewport metadata, got %#v", payload)
	}
	if payload["agentKey"] != "agent_1" {
		t.Fatalf("expected agentKey on approval awaiting.ask, got %#v", payload)
	}
	approvals, _ := payload["approvals"].([]any)
	if len(approvals) != 1 {
		t.Fatalf("expected approvals in approval awaiting.ask, got %#v", payload)
	}
}

func TestDispatcherDefaultsQuestionAwaitAskViewport(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	events := dispatcher.Dispatch(AwaitAsk{
		AwaitingID: "tool_1",
		Mode:       "question",
		Timeout:    120000,
		RunID:      "run_1",
		Questions: []any{
			map[string]any{"id": "q1", "question": "Need confirmation", "type": "text"},
		},
	})
	assertEventTypes(t, events, "awaiting.ask")
	payload := events[0].ToData()
	if payload["viewportType"] != "builtin" || payload["viewportKey"] != "question" {
		t.Fatalf("expected builtin question viewport metadata, got %#v", payload)
	}
}

func TestDispatcherEmitsPlanModeAwaitAsk(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:    "run_1",
		ChatID:   "chat_1",
		AgentKey: "agent_1",
	})

	events := dispatcher.Dispatch(AwaitAsk{
		AwaitingID: "run_1_coder_plan_confirm_1",
		Mode:       "plan",
		Timeout:    0,
		RunID:      "run_1",
		Plan: map[string]any{
			"id":         "confirm",
			"planningId": "run_1_planning_1",
			"title":      "实施此计划？",
		},
	})
	assertEventTypes(t, events, "awaiting.ask")
	payload := events[0].ToData()
	if payload["viewportType"] != "builtin" || payload["viewportKey"] != "plan" {
		t.Fatalf("expected builtin plan viewport metadata, got %#v", payload)
	}
	if _, ok := payload["approvals"]; ok {
		t.Fatalf("did not expect approvals for plan awaiting.ask, got %#v", payload)
	}
	plan, _ := payload["plan"].(map[string]any)
	if plan["id"] != "confirm" || plan["planningId"] != "run_1_planning_1" {
		t.Fatalf("unexpected plan awaiting.ask payload %#v", payload)
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
				"form": map[string]any{
					"applicant":  "Lin",
					"days":       3,
					"leave_type": "年假",
				},
			},
		},
	})
	assertEventTypes(t, events, "awaiting.ask")
	payload := events[0].ToData()
	if payload["viewportType"] != "html" || payload["viewportKey"] != "leave_form" {
		t.Fatalf("expected html form viewport metadata, got %#v", payload)
	}
	forms, _ := payload["forms"].([]any)
	if len(forms) != 1 {
		t.Fatalf("expected forms in form awaiting.ask, got %#v", payload)
	}
	form := forms[0].(map[string]any)
	if _, exists := form["command"]; exists {
		t.Fatalf("did not expect form command in awaiting.ask payload, got %#v", payload)
	}
	formPayload, _ := form["form"].(map[string]any)
	applicant, _ := formPayload["applicant"].(string)
	if formPayload == nil || applicant != "Lin" || formPayload["days"] != 3 {
		t.Fatalf("expected form data in form awaiting.ask, got %#v", payload)
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
		"agentKey":   "agent_1",
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
		`"agentKey":"agent_1"`,
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
				"form": map[string]any{
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
	formsIndex := strings.Index(text, `"forms":[{"form":{"applicant":"Lin"},"id":"form-1","title":"mock 请假申请"}]`)
	timestampIndex := strings.Index(text, `"timestamp":`)
	if formsIndex < 0 || timestampIndex < 0 || formsIndex >= timestampIndex {
		t.Fatalf("expected forms before timestamp in %s", text)
	}
	if strings.Contains(text, `"viewportPayload":`) {
		t.Fatalf("did not expect viewportPayload in %s", text)
	}
}

func TestEventDataMarshalsAwaitAskWithPlanBeforeTimestamp(t *testing.T) {
	event := NewEvent("awaiting.ask", map[string]any{
		"awaitingId": "run_1_coder_plan_confirm_1",
		"mode":       "plan",
		"timeout":    0,
		"runId":      "run_1",
		"plan": map[string]any{
			"id":         "confirm",
			"planningId": "run_1_planning_1",
		},
	})
	data, err := json.Marshal(event.Data())
	if err != nil {
		t.Fatalf("marshal event data: %v", err)
	}
	text := string(data)
	planIndex := strings.Index(text, `"plan":{"id":"confirm","planningId":"run_1_planning_1"}`)
	timestampIndex := strings.Index(text, `"timestamp":`)
	if planIndex < 0 || timestampIndex < 0 || planIndex >= timestampIndex {
		t.Fatalf("expected plan before timestamp in %s", text)
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

func TestDispatcherEmitsAwaitingAnswerForPlanMode(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	events := dispatcher.Dispatch(AwaitingAnswer{
		AwaitingID: "run_1_coder_plan_confirm_1",
		Answer: map[string]any{
			"mode":   "plan",
			"status": "answered",
			"plan": map[string]any{
				"id":         "confirm",
				"planningId": "run_1_planning_1",
				"decision":   "reject",
				"reason":     "请补充测试范围",
			},
		},
	})
	assertEventTypes(t, events, "awaiting.answer")
	payload := events[0].ToData()
	if payload["mode"] != "plan" || payload["status"] != "answered" {
		t.Fatalf("unexpected plan awaiting.answer payload %#v", payload)
	}
	plan, _ := payload["plan"].(map[string]any)
	if plan["id"] != "confirm" || plan["planningId"] != "run_1_planning_1" || plan["decision"] != "reject" || plan["reason"] != "请补充测试范围" {
		t.Fatalf("unexpected formatted plan answer %#v", payload)
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
					"id":       "form-1",
					"decision": "approve",
					"form": map[string]any{
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
	formPayload, _ := forms[0]["form"].(map[string]any)
	if forms[0]["decision"] != "approve" || formPayload["applicant_id"] != "E1001" || formPayload["days"] != 2 {
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
				"id":       "form-1",
				"decision": "approve",
				"form": map[string]any{
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
		`"forms":[{"decision":"approve","form":{"applicant_id":"E1001"},"id":"form-1"}]`,
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

func TestEventDataMarshalsAwaitingAnswerPlanSubmitWithContractKeyOrder(t *testing.T) {
	event := NewEvent("awaiting.answer", map[string]any{
		"awaitingId": "run_1_coder_plan_confirm_1",
		"mode":       "plan",
		"status":     "answered",
		"plan": map[string]any{
			"id":         "confirm",
			"planningId": "run_1_planning_1",
			"decision":   "approve",
		},
	})
	event.Seq = 14
	data, err := json.Marshal(event.Data())
	if err != nil {
		t.Fatalf("marshal event data: %v", err)
	}
	text := string(data)
	order := []string{
		`"seq":14`,
		`"type":"awaiting.answer"`,
		`"awaitingId":"run_1_coder_plan_confirm_1"`,
		`"mode":"plan"`,
		`"status":"answered"`,
		`"plan":{"decision":"approve","id":"confirm","planningId":"run_1_planning_1"}`,
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
