package stream

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/apperrors"
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
	assertDurationMsPresent(t, resultEvents[0])
}

func TestDispatcherEmitsAwaitingAnswerDuration(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	askEvents := dispatcher.Dispatch(AwaitAsk{
		AwaitingID: "tool_1",
		Mode:       "approval",
		Timeout:    120,
		RunID:      "run_1",
		Approvals: []any{
			map[string]any{"id": "cmd-1", "command": "Proceed?"},
		},
	})
	assertEventTypes(t, askEvents, "awaiting.ask")

	answerEvents := dispatcher.Dispatch(AwaitingAnswer{
		AwaitingID: "tool_1",
		Answer: map[string]any{
			"mode":   "approval",
			"status": "answered",
			"approvals": []any{
				map[string]any{
					"id":       "cmd-1",
					"decision": "approve",
				},
			},
		},
	})
	assertEventTypes(t, answerEvents, "awaiting.answer")
	assertDurationMsPresent(t, answerEvents[0])
}

func TestDispatcherEmitsRunActivity(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	events := dispatcher.Dispatch(InputRunActivity{
		TaskID:  "task_1",
		ChatID:  "chat_1",
		Phase:   "model_call",
		Status:  "retrying",
		Message: "retrying",
		Retry: map[string]any{
			"attempt":        2,
			"maxAttempts":    4,
			"reason":         "model_stream_idle_timeout",
			"timeoutSeconds": int64(60),
			"elapsedMs":      int64(60001),
		},
	})
	assertEventTypes(t, events, "run.activity")
	data := events[0].ToData()
	if data["runId"] != "run_1" || data["chatId"] != "chat_1" || data["taskId"] != "task_1" {
		t.Fatalf("unexpected identity payload %#v", data)
	}
	if data["status"] != "retrying" || data["message"] != "retrying" {
		t.Fatalf("unexpected activity payload %#v", data)
	}
	retry, _ := data["retry"].(map[string]any)
	if retry["attempt"] != 2 || retry["maxAttempts"] != 4 || retry["timeoutSeconds"] != int64(60) {
		t.Fatalf("unexpected retry payload %#v", data)
	}
}

func TestDispatcherEmitsFileChangeOnToolEndAndSnapshot(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	_ = dispatcher.Dispatch(ToolArgs{
		ToolID:     "tool_1",
		ToolName:   "file_edit",
		Delta:      "{}",
		ChunkIndex: 0,
	})
	fileChange := map[string]any{
		"filePath":  "/tmp/app.go",
		"operation": "edit",
		"lineStats": map[string]any{
			"addedLines":   1,
			"deletedLines": 1,
			"editedLines":  1,
		},
	}
	events := dispatcher.Dispatch(ToolEnd{
		ToolID:     "tool_1",
		FileChange: fileChange,
	})
	assertEventTypes(t, events, "tool.end", "tool.snapshot")
	for _, event := range events {
		got, _ := event.ToData()["fileChange"].(map[string]any)
		if !reflect.DeepEqual(got, fileChange) {
			t.Fatalf("expected fileChange on %s, got %#v", event.Type, event.ToData())
		}
	}
}

func TestDispatcherEmitsFileChangeOnToolResult(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	fileChange := map[string]any{
		"filePath":  "/tmp/app.go",
		"operation": "edit",
		"lineStats": map[string]any{
			"addedLines":   1,
			"deletedLines": 1,
			"editedLines":  1,
		},
	}
	events := dispatcher.Dispatch(ToolResult{
		ToolID:     "tool_1",
		ToolName:   "file_edit",
		Result:     map[string]any{"status": "edited"},
		FileChange: fileChange,
	})
	assertEventTypes(t, events, "tool.result")
	got, _ := events[0].ToData()["fileChange"].(map[string]any)
	if !reflect.DeepEqual(got, fileChange) {
		t.Fatalf("expected fileChange on tool.result, got %#v", events[0].ToData())
	}
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

func TestDispatcherKeepsTaskContentOpenAcrossOtherTaskTerminal(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})
	dispatcher.Dispatch(TaskStart{
		TaskID:      "task_1",
		RunID:       "run_1",
		TaskName:    "检查",
		SubAgentKey: "reviewer",
		MainToolID:  "tool_main_1",
	})
	dispatcher.Dispatch(TaskStart{
		TaskID:      "task_2",
		RunID:       "run_1",
		TaskName:    "写作",
		SubAgentKey: "writer",
		MainToolID:  "tool_main_1",
	})
	events := dispatcher.Dispatch(ContentDelta{
		ContentID: "task_2_c_1",
		TaskID:    "task_2",
		Delta:     "前半句",
	})
	assertEventTypes(t, events, "content.start", "content.delta")

	events = dispatcher.Dispatch(ToolArgs{
		ToolID:     "tool_task_1",
		TaskID:     "task_1",
		ToolName:   "datetime",
		Delta:      "{}",
		ChunkIndex: 0,
	})
	assertEventTypes(t, events, "tool.start", "tool.args")

	events = dispatcher.Dispatch(TaskComplete{TaskID: "task_1"})
	assertEventTypes(t, events, "tool.end", "tool.snapshot", "task.complete")

	events = dispatcher.Dispatch(ContentDelta{
		ContentID: "task_2_c_1",
		TaskID:    "task_2",
		Delta:     "后半句",
	})
	assertEventTypes(t, events, "content.delta")
	if events[0].Data().String("contentId") != "task_2_c_1" {
		t.Fatalf("expected continued task_2 content delta, got %#v", events[0].ToData())
	}
}

func TestDispatcherKeepsMainContentOpenAcrossChildTaskTerminal(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})
	events := dispatcher.Dispatch(ContentDelta{
		ContentID: "run_1_c_1",
		Delta:     "主输出",
	})
	assertEventTypes(t, events, "content.start", "content.delta")

	dispatcher.Dispatch(TaskStart{
		TaskID:      "task_child",
		RunID:       "run_1",
		TaskName:    "子任务",
		SubAgentKey: "writer",
		MainToolID:  "tool_main_1",
	})
	events = dispatcher.Dispatch(ContentDelta{
		ContentID: "task_child:final",
		TaskID:    "task_child",
		Delta:     "子输出",
	})
	assertEventTypes(t, events, "content.start", "content.delta")

	events = dispatcher.Dispatch(TaskComplete{TaskID: "task_child"})
	assertEventTypes(t, events, "content.end", "content.snapshot", "task.complete")

	events = dispatcher.Dispatch(ContentDelta{
		ContentID: "run_1_c_1",
		Delta:     "继续",
	})
	assertEventTypes(t, events, "content.delta")
	if events[0].Data().String("contentId") != "run_1_c_1" {
		t.Fatalf("expected continued main content delta, got %#v", events[0].ToData())
	}

	events = dispatcher.Complete()
	assertEventTypes(t, events, "content.end", "content.snapshot", "run.complete")
	if got := events[1].Data().String("text"); got != "主输出继续" {
		t.Fatalf("expected main content snapshot to include both deltas, got %#v", events[1].ToData())
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

	start := dispatcher.Dispatch(PlanningStart{PlanningID: "plan-run_1"})
	assertEventTypes(t, start, "planning.start")
	if got := start[0].Data().Payload; len(got) != 1 || got["planningId"] != "plan-run_1" {
		t.Fatalf("unexpected planning.start payload %#v", start[0].Data().Map())
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

	end := dispatcher.Dispatch(PlanningEnd{PlanningID: "plan-run_1"})
	assertEventTypes(t, end, "planning.end")
	if got := end[0].Data().Payload; len(got) != 1 || got["planningId"] != "plan-run_1" {
		t.Fatalf("unexpected planning.end payload %#v", end[0].Data().Map())
	}
}

func TestDispatcherIncludesTaskIDOnDebugEvents(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	llmEvents := dispatcher.Dispatch(InputDebugLLMChat{
		TaskID:                          "task_sub_1",
		ChatID:                          "chat_1",
		ProviderKey:                     "mock",
		ProviderEndpoint:                "https://api.example.test/v1/chat/completions",
		ModelKey:                        "mock-model",
		ModelID:                         "mock-model-id",
		ReasoningEffort:                 "HIGH",
		Status:                          "ok",
		RunSeq:                          2,
		TraceFile:                       "chat_1/.llm-records/run_1_002.json",
		TraceURL:                        "/api/chat/llm-trace?file=chat_1%2F.llm-records%2Frun_1_002.json",
		SystemRef:                       map[string]any{"cacheKey": "react:main", "fingerprint": "sha256:test"},
		ContextWindow:                   128000,
		CurrentContextSize:              100,
		EstimatedNextCallSize:           150,
		LLMReturnPromptTokens:           7,
		LLMReturnCompletionTokens:       3,
		LLMReturnTotalTokens:            10,
		LLMReturnCachedTokens:           4,
		LLMReturnReasoningTokens:        2,
		LLMReturnPromptCacheHitTokens:   4,
		LLMReturnPromptCacheMissTokens:  3,
		LLMReturnLLMChatCompletionCount: 1,
		LLMReturnToolCallCount:          2,
		LLMReturnFirstTokenLatencyMs:    820,
		LLMReturnGenerationDurationMs:   2380,
		RunPromptTokens:                 107,
		RunCompletionTokens:             53,
		RunTotalTokens:                  160,
		RunCachedTokens:                 68,
		RunReasoningTokens:              14,
		RunPromptCacheHitTokens:         68,
		RunPromptCacheMissTokens:        39,
		RunLLMChatCompletionCount:       2,
		RunToolCallCount:                4,
		RunFirstTokenLatencyTotalMs:     1820,
		RunFirstTokenLatencyCount:       2,
		RunGenerationDurationMs:         5000,
	})
	assertEventTypes(t, llmEvents, "debug.llmChat")
	llmData := llmEvents[0].Data()
	if got := llmData.String("taskId"); got != "task_sub_1" {
		t.Fatalf("expected debug.llmChat taskId, got %#v", llmEvents[0].ToData())
	}
	llmPayload, _ := llmData.Value("data").(map[string]any)
	trace, _ := llmPayload["trace"].(map[string]any)
	if trace["file"] != "chat_1/.llm-records/run_1_002.json" || trace["url"] != "/api/chat/llm-trace?file=chat_1%2F.llm-records%2Frun_1_002.json" {
		t.Fatalf("unexpected trace payload %#v", llmPayload)
	}
	if llmPayload["status"] != "ok" || llmPayload["runSeq"] != 2 {
		t.Fatalf("unexpected llm call metadata %#v", llmPayload)
	}
	systemRef, _ := llmPayload["systemRef"].(map[string]any)
	if systemRef["cacheKey"] != "react:main" {
		t.Fatalf("expected systemRef in debug.llmChat, got %#v", llmPayload)
	}
	llmUsageEnvelope, _ := llmPayload["usage"].(map[string]any)
	llmReturnUsage, _ := llmUsageEnvelope["llmReturnUsage"].(map[string]any)
	llmPromptDetails, _ := llmReturnUsage["promptTokensDetails"].(map[string]any)
	llmCompletionDetails, _ := llmReturnUsage["completionTokensDetails"].(map[string]any)
	if llmPromptDetails["cacheHitTokens"] != 4 || llmPromptDetails["cacheMissTokens"] != 3 ||
		llmCompletionDetails["reasoningTokens"] != 2 ||
		llmReturnUsage["llmChatCompletionCount"] != 1 ||
		llmReturnUsage["toolCallCount"] != 2 {
		t.Fatalf("expected detailed debug.llmChat usage, got %#v", llmUsageEnvelope)
	}
	llmReturnTiming, _ := llmReturnUsage["timing"].(map[string]any)
	if intValue(llmReturnTiming["firstTokenLatencyMs"]) != 820 ||
		intValue(llmReturnTiming["generationDurationMs"]) != 2380 {
		t.Fatalf("expected timing debug.llmChat usage, got %#v", llmUsageEnvelope)
	}
	if _, exists := llmReturnTiming["outputTokensPerSecond"]; exists {
		t.Fatalf("did not expect derived tokens/s in debug.llmChat timing, got %#v", llmReturnTiming)
	}
	runUsage, _ := llmUsageEnvelope["runUsage"].(map[string]any)
	runTiming, _ := runUsage["timing"].(map[string]any)
	if intValue(runTiming["firstTokenLatencyTotalMs"]) != 1820 ||
		intValue(runTiming["firstTokenLatencyCount"]) != 2 ||
		intValue(runTiming["generationDurationMs"]) != 5000 {
		t.Fatalf("expected aggregated run timing in debug.llmChat usage, got %#v", llmUsageEnvelope)
	}
	if _, exists := runTiming["firstTokenLatencyMs"]; exists {
		t.Fatalf("did not expect aggregate first token average in debug.llmChat timing, got %#v", runTiming)
	}
}

func TestDispatcherTerminalUsageIncludesLLMChatCompletionCountWithoutTokens(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	dispatcher.Dispatch(InputUsageSnapshot{
		ChatID:                          "chat_1",
		ModelKey:                        "mock",
		RunLLMChatCompletionCount:       1,
		RunToolCallCount:                3,
		LLMReturnLLMChatCompletionCount: 1,
		LLMReturnToolCallCount:          3,
	})
	dispatcher.Dispatch(InputRunComplete{FinishReason: "stop"})
	events := dispatcher.Complete()
	assertEventTypes(t, events, "run.complete")

	usage, _ := events[0].Data().Value("usage").(map[string]any)
	if usage == nil || usage["llmChatCompletionCount"] != 1 || usage["toolCallCount"] != 3 {
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
		ReasoningEffort:                 "HIGH",
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
		LLMReturnToolCallCount:          2,
		LLMReturnFirstTokenLatencyMs:    700,
		LLMReturnGenerationDurationMs:   2300,
		RunPromptTokens:                 300,
		RunCompletionTokens:             75,
		RunTotalTokens:                  375,
		RunCachedTokens:                 128,
		RunReasoningTokens:              24,
		RunPromptCacheHitTokens:         128,
		RunPromptCacheMissTokens:        172,
		RunLLMChatCompletionCount:       2,
		RunToolCallCount:                5,
		RunFirstTokenLatencyTotalMs:     1900,
		RunFirstTokenLatencyCount:       2,
		RunGenerationDurationMs:         4800,
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
	if current["toolCallCount"] != 2 {
		t.Fatalf("expected current toolCallCount, got %#v", usage)
	}
	if current["modelKey"] != "deepseek-v4-pro" {
		t.Fatalf("expected current modelKey, got %#v", usage)
	}
	if current["reasoningEffort"] != "HIGH" {
		t.Fatalf("expected current reasoningEffort, got %#v", usage)
	}
	currentTiming, _ := current["timing"].(map[string]any)
	if intValue(currentTiming["firstTokenLatencyMs"]) != 700 ||
		intValue(currentTiming["generationDurationMs"]) != 2300 {
		t.Fatalf("expected current timing usage, got %#v", usage)
	}
	if _, exists := currentTiming["firstTokenLatencyTotalMs"]; exists {
		t.Fatalf("did not expect current first token total in usage.snapshot timing, got %#v", currentTiming)
	}
	if _, exists := currentTiming["firstTokenLatencyCount"]; exists {
		t.Fatalf("did not expect current first token count in usage.snapshot timing, got %#v", currentTiming)
	}
	if _, exists := currentTiming["outputTokensPerSecond"]; exists {
		t.Fatalf("did not expect derived tokens/s in usage.snapshot current timing, got %#v", currentTiming)
	}
	runPromptDetails, _ := run["promptTokensDetails"].(map[string]any)
	if runPromptDetails["cacheHitTokens"] != 128 || runPromptDetails["cacheMissTokens"] != 172 || run["llmChatCompletionCount"] != 2 || run["toolCallCount"] != 5 {
		t.Fatalf("expected detailed run usage, got %#v", usage)
	}
	runTiming, _ := run["timing"].(map[string]any)
	if intValue(runTiming["firstTokenLatencyTotalMs"]) != 1900 ||
		intValue(runTiming["firstTokenLatencyCount"]) != 2 ||
		intValue(runTiming["generationDurationMs"]) != 4800 {
		t.Fatalf("expected run timing usage, got %#v", usage)
	}
	if _, exists := runTiming["firstTokenLatencyMs"]; exists {
		t.Fatalf("did not expect aggregate first token average in usage.snapshot timing, got %#v", runTiming)
	}
	if _, exists := runTiming["outputTokensPerSecond"]; exists {
		t.Fatalf("did not expect derived tokens/s in usage.snapshot run timing, got %#v", runTiming)
	}
	if _, exists := run["modelKey"]; exists {
		t.Fatalf("did not expect run modelKey, got %#v", usage)
	}
	if _, exists := data.Map()["model"]; exists {
		t.Fatalf("did not expect top-level model, got %#v", data.Map())
	}
	cw, _ := data.Value("contextWindow").(map[string]any)
	if cw["maxSize"] != 128000 || cw["currentSize"] != 100 || cw["estimatedNextCallSize"] != 200 {
		t.Fatalf("unexpected context window %#v", cw)
	}
	if cw["modelKey"] != "deepseek-v4-pro" || cw["reasoningEffort"] != "HIGH" {
		t.Fatalf("expected context window model metadata to match current usage, got %#v", cw)
	}
}

func TestDispatcherUsageSnapshotIncludesZeroToolCallCounts(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	events := dispatcher.Dispatch(InputUsageSnapshot{
		ChatID:                          "chat_1",
		LLMReturnPromptTokens:           100,
		LLMReturnCompletionTokens:       25,
		LLMReturnTotalTokens:            125,
		LLMReturnLLMChatCompletionCount: 1,
		LLMReturnGenerationDurationMs:   1000,
		RunPromptTokens:                 100,
		RunCompletionTokens:             25,
		RunTotalTokens:                  125,
		RunLLMChatCompletionCount:       1,
		RunGenerationDurationMs:         1000,
	})

	assertEventTypes(t, events, "usage.snapshot")
	usage, _ := events[0].Data().Value("usage").(map[string]any)
	current, _ := usage["current"].(map[string]any)
	run, _ := usage["run"].(map[string]any)
	if current["toolCallCount"] != 0 || run["toolCallCount"] != 0 {
		t.Fatalf("expected zero tool call counts in usage.snapshot, got %#v", usage)
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
		t.Fatalf("did not expect hitl key, got %#v", payload)
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
		Timeout:      120,
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
		Timeout:    120,
		RunID:      "run_1",
		Approvals: []any{
			map[string]any{
				"id":            "cmd-1",
				"command":       "git push origin main",
				"description":   "推送主分支",
				"options":       []any{map[string]any{"decision": "approve"}},
				"allowFreeText": true,
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
		Timeout:    120,
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
		RunID:      "run_1",
		Plan: map[string]any{
			"id":         "confirm",
			"planningId": "run_1_planning_1",
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
	if _, ok := payload["timeout"]; ok {
		t.Fatalf("did not expect timeout for planning confirmation, got %#v", payload)
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
		Timeout:    120,
		RunID:      "run_1",
		Approvals: []any{
			map[string]any{
				"id":            "tool_1",
				"command":       "chmod 777 ~/a.sh",
				"description":   "放开 a.sh 权限",
				"options":       []any{map[string]any{"decision": "approve"}},
				"allowFreeText": true,
			},
		},
	})
	assertEventTypes(t, first, "awaiting.ask")

	second := dispatcher.Dispatch(AwaitAsk{
		AwaitingID: "await_1",
		Mode:       "approval",
		Timeout:    120,
		RunID:      "run_1",
		Approvals: []any{
			map[string]any{
				"id":            "tool_1",
				"command":       "chmod 777 ~/a.sh",
				"description":   "放开 a.sh 权限",
				"options":       []any{map[string]any{"decision": "approve"}},
				"allowFreeText": true,
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
		Timeout:      120,
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

func TestDispatcherFailPreservesStructuredProviderError(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	events := dispatcher.Fail(apperrors.New(
		apperrors.CodeProviderQuotaExhausted,
		"model request failed with status 429: api key quota exhausted",
		apperrors.WithStatus(429),
	))
	assertEventTypes(t, events, "run.error")

	last := events[len(events)-1].ToData()
	errPayload, _ := last["error"].(map[string]any)
	if errPayload["code"] != string(apperrors.CodeProviderQuotaExhausted) {
		t.Fatalf("expected provider code, got %#v", errPayload)
	}
	if errPayload["category"] != string(apperrors.CategoryModel) || errPayload["status"] != 429 || errPayload["retryable"] != false {
		t.Fatalf("unexpected provider error metadata %#v", errPayload)
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

func TestEventDataMarshalsToolResultWithContractKeyOrder(t *testing.T) {
	event := NewEvent("tool.result", map[string]any{
		"toolId":     "tool_1",
		"toolName":   "bash",
		"result":     "ok",
		"durationMs": int64(42),
		"approval": map[string]any{
			"decision": "approve",
		},
	})
	event.Seq = 8
	data, err := json.Marshal(event.Data())
	if err != nil {
		t.Fatalf("marshal event data: %v", err)
	}
	text := string(data)
	order := []string{
		`"seq":8`,
		`"type":"tool.result"`,
		`"toolId":"tool_1"`,
		`"toolName":"bash"`,
		`"result":"ok"`,
		`"durationMs":42`,
		`"approval":{"decision":"approve"}`,
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
		"timeout":    120,
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
		`"timeout":120`,
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
		"timeout":      120,
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
		"timeout":    120,
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

func TestDispatcherPreservesAwaitingAnswerTimeoutErrorDetails(t *testing.T) {
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
				"code":           "timeout",
				"message":        "等待项已超时（60秒）。原因：等待问题回复，超过配置的 60 秒未收到有效提交。",
				"timeoutSeconds": int64(60),
				"elapsedSeconds": int64(61),
				"reason":         "submit_not_received_before_timeout",
			},
		},
	})
	assertEventTypes(t, events, "awaiting.answer")
	errPayload, _ := events[0].ToData()["error"].(map[string]any)
	if errPayload["code"] != "timeout" ||
		errPayload["message"] != "等待项已超时（60秒）。原因：等待问题回复，超过配置的 60 秒未收到有效提交。" ||
		errPayload["timeoutSeconds"] != int64(60) ||
		errPayload["elapsedSeconds"] != int64(61) ||
		errPayload["reason"] != "submit_not_received_before_timeout" {
		t.Fatalf("unexpected timeout error payload %#v", errPayload)
	}
}

func TestEventDataMarshalsAwaitingAnswerWithContractKeyOrder(t *testing.T) {
	event := NewEvent("awaiting.answer", map[string]any{
		"awaitingId": "tool_1",
		"mode":       "question",
		"status":     "error",
		"durationMs": int64(42),
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
		`"durationMs":42`,
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

func assertDurationMsPresent(t *testing.T, event StreamEvent) {
	t.Helper()
	value, ok := event.Payload["durationMs"]
	if !ok {
		t.Fatalf("expected durationMs on %s, got %#v", event.Type, event.Payload)
	}
	duration, ok := value.(int64)
	if !ok {
		t.Fatalf("expected int64 durationMs on %s, got %#v", event.Type, value)
	}
	if duration < 0 {
		t.Fatalf("expected non-negative durationMs on %s, got %d", event.Type, duration)
	}
}
