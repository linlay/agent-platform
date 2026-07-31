package chat

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"agent-platform/internal/stream"
)

func TestLegacyFailedRunDropsLastMalformedUnmatchedToolTurn(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	defer store.Close()

	const chatID = "chat-legacy-truncated-tool"
	const runID = "run-legacy-truncated-tool"
	if _, _, err := store.EnsureChat(chatID, "agent", "", "write it"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	startedAt := testEpochMillis(1)
	if err := appendQueryLineForTest(store, chatID, QueryLine{
		Type:      "query",
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: startedAt,
		Query:     map[string]any{"role": "user", "message": "write it"},
		Messages:  []map[string]any{{"role": "user", "content": "write it", "ts": startedAt}},
	}); err != nil {
		t.Fatalf("append query: %v", err)
	}
	if err := appendStepLineForTest(store, chatID, StepLine{
		Type:      StepLineTypeReact,
		ChatID:    chatID,
		RunID:     runID,
		Seq:       1,
		UpdatedAt: startedAt + 1,
		Messages: []StoredMessage{{
			Role: "assistant",
			ToolCalls: []StoredToolCall{{
				ID:     "call_truncated",
				Type:   "function",
				ToolID: "call_truncated",
				Function: StoredFunction{
					Name:      "file_write",
					Arguments: `{"path":"notes.md","content":"cut off`,
				},
			}},
			Ts: int64Ptr(startedAt + 1),
		}},
	}); err != nil {
		t.Fatalf("append malformed react: %v", err)
	}
	if err := completeRunForTest(store, RunCompletion{
		ChatID:          chatID,
		RunID:           runID,
		AgentKey:        "agent",
		InitialMessage:  "write it",
		FinishReason:    "error",
		UpdatedAtMillis: startedAt + 2,
	}); err != nil {
		t.Fatalf("complete run: %v", err)
	}

	messages, err := store.LoadRawMessages(chatID, DefaultHistoryRunWindow)
	if err != nil {
		t.Fatalf("load logical history: %v", err)
	}
	if len(messages) != 1 || messages[0]["content"] != "write it" {
		t.Fatalf("malformed model turn was not removed: %#v", messages)
	}
	raw, err := store.LoadJSONLContent(chatID)
	if err != nil {
		t.Fatalf("load audit jsonl: %v", err)
	}
	if !strings.Contains(raw, "call_truncated") {
		t.Fatalf("raw audit jsonl must retain the discarded record: %s", raw)
	}
}

func TestLegacyFailedRunRejectsValidUnmatchedToolCall(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	defer store.Close()

	const chatID = "chat-legacy-ambiguous-tool"
	const runID = "run-legacy-ambiguous-tool"
	if _, _, err := store.EnsureChat(chatID, "agent", "", "run it"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	startedAt := testEpochMillis(10)
	if err := appendStepLineForTest(store, chatID, StepLine{
		Type:      StepLineTypeReact,
		ChatID:    chatID,
		RunID:     runID,
		Seq:       1,
		UpdatedAt: startedAt + 1,
		Messages: []StoredMessage{{
			Role: "assistant",
			ToolCalls: []StoredToolCall{{
				ID:       "call_unknown_side_effect",
				Type:     "function",
				ToolID:   "call_unknown_side_effect",
				Function: StoredFunction{Name: "bash", Arguments: `{"command":"touch marker"}`},
			}},
			Ts: int64Ptr(startedAt + 1),
		}},
	}); err != nil {
		t.Fatalf("append ambiguous react: %v", err)
	}
	if err := completeRunForTest(store, RunCompletion{
		ChatID:          chatID,
		RunID:           runID,
		AgentKey:        "agent",
		FinishReason:    "error",
		UpdatedAtMillis: startedAt + 2,
	}); err != nil {
		t.Fatalf("complete run: %v", err)
	}

	if _, err := store.LoadRawMessages(chatID, DefaultHistoryRunWindow); !errors.Is(err, ErrChatHistoryIncomplete) {
		t.Fatalf("expected chat_history_incomplete, got %v", err)
	}
}

func TestLegacyCancelledRunSynthesizesInterruptedAwaitingInLogicalViewOnly(t *testing.T) {
	const (
		chatID     = "chat-legacy-cancelled-awaiting"
		runID      = "run-legacy-cancelled-awaiting"
		toolID     = "call-waiting"
		awaitingID = "await-batch"
	)
	startedAt := testEpochMillis(15)
	completedAt := startedAt + 250
	lines := []map[string]any{{
		"chatId":    chatID,
		"runId":     runID,
		"updatedAt": startedAt,
		"_type":     StepLineTypeReact,
		"seq":       2,
		"messages": []any{map[string]any{
			"role": "assistant",
			"tool_calls": []any{map[string]any{
				"id":   toolID,
				"type": "function",
				"function": map[string]any{
					"name":      "bash",
					"arguments": `{"command":"touch must-not-run"}`,
				},
			}},
		}},
		"awaiting": []any{map[string]any{
			"type":       "awaiting.ask",
			"awaitingId": awaitingID,
			"mode":       "approval",
			"timestamp":  startedAt,
			"approvals": []any{map[string]any{
				"id": toolID,
			}},
		}},
	}}

	logical, err := filterLegacyIncompleteModelTurnsWithRuns(lines, map[string]legacyRepairableRunState{
		runID: {finishReason: "cancel", completedAt: completedAt},
	})
	if err != nil {
		t.Fatalf("recover cancelled awaiting: %v", err)
	}
	if len(lines) != 1 || len(logical) != 3 {
		t.Fatalf("logical recovery must append two in-memory lines only, raw=%d logical=%d", len(lines), len(logical))
	}
	answer := mapValue(logical[1]["answer"])
	if logical[1]["_type"] != "submit" ||
		answer["awaitingId"] != awaitingID ||
		answer["status"] != "error" ||
		mapValue(answer["error"])["code"] != "run_interrupted" {
		t.Fatalf("unexpected synthetic awaiting answer: %#v", logical[1])
	}
	if toIntFromKeys(logical[2], "seq") != 2 || logical[2]["_type"] != StepLineTypeReactTool {
		t.Fatalf("synthetic tool result must reuse react seq: %#v", logical[2])
	}
	messages := anyMessageSlice(logical[2]["messages"])
	if len(messages) != 1 || messages[0]["tool_call_id"] != toolID || messages[0]["name"] != "bash" {
		t.Fatalf("unexpected synthetic tool message: %#v", messages)
	}
	content := anyMessageSlice(messages[0]["content"])
	if len(content) != 1 {
		t.Fatalf("unexpected synthetic tool content: %#v", messages[0]["content"])
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(stringValue(content[0]["text"])), &payload); err != nil {
		t.Fatalf("decode synthetic tool payload: %v", err)
	}
	if payload["error"] != "run_interrupted" ||
		payload["executed"] != false ||
		payload["exitCode"] != float64(-1) ||
		payload["awaitingId"] != awaitingID ||
		payload["output"] != "tool execution was cancelled before approval" {
		t.Fatalf("unexpected synthetic tool payload: %#v", payload)
	}
}

func TestLoadChatRecoversKnownCancelledApprovalRegressionWithoutRewritingJSONL(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	defer store.Close()
	const (
		chatID     = "baa11e01-29f9-41e1-bed4-c738a175817c"
		runID      = "ms8gc5mt"
		toolID     = "call_019fb677275874f3ad6bde01"
		awaitingID = "await_batch_ms8gc5mt_2"
	)
	const (
		awaitingAt  int64 = 1785472691381
		completedAt int64 = 1785472821266
	)
	if _, _, err := store.EnsureChat(chatID, "zenmi", "", "scan agents"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := store.OnRunStarted(RunStart{
		ChatID:          chatID,
		RunID:           runID,
		AgentKey:        "zenmi",
		AgentMode:       "REACT",
		InitialMessage:  "scan agents",
		StartedAtMillis: awaitingAt - 10_000,
	}); err != nil {
		t.Fatalf("start run: %v", err)
	}
	if err := store.AppendStepLine(chatID, StepLine{
		Type:      StepLineTypeReact,
		ChatID:    chatID,
		RunID:     runID,
		Seq:       2,
		UpdatedAt: awaitingAt,
		Messages: []StoredMessage{{
			Role: "assistant",
			ToolCalls: []StoredToolCall{{
				ID:     toolID,
				Type:   "function",
				ToolID: toolID,
				Function: StoredFunction{
					Name:      "bash",
					Arguments: `{"command":"for dir in agents; do head agent.yml; done"}`,
				},
			}},
			Ts: int64Ptr(awaitingAt),
		}},
		Awaiting: []map[string]any{{
			"agentKey":   "zenmi",
			"type":       "awaiting.ask",
			"awaitingId": awaitingID,
			"mode":       "approval",
			"runId":      runID,
			"timestamp":  awaitingAt,
			"approvals": []any{map[string]any{
				"id":          toolID,
				"description": "读取各智能体 agent.yml",
			}},
		}},
	}); err != nil {
		t.Fatalf("append regression react: %v", err)
	}
	if err := store.OnRunCompleted(RunCompletion{
		ChatID:          chatID,
		RunID:           runID,
		AgentKey:        "zenmi",
		AgentMode:       "REACT",
		InitialMessage:  "scan agents",
		FinishReason:    "cancel",
		StartedAtMillis: awaitingAt - 10_000,
		UpdatedAtMillis: completedAt,
	}); err != nil {
		t.Fatalf("complete cancelled run: %v", err)
	}
	before, err := store.LoadJSONLContent(chatID)
	if err != nil {
		t.Fatalf("load raw JSONL before replay: %v", err)
	}

	detail, err := store.LoadChat(chatID)
	if err != nil {
		t.Fatalf("load recovered chat: %v", err)
	}
	eventCounts := map[string]int{}
	for _, event := range detail.Events {
		eventCounts[event.Type]++
	}
	if eventCounts["awaiting.answer"] != 1 ||
		eventCounts["tool.result"] != 1 ||
		eventCounts["run.cancel"] != 1 {
		t.Fatalf("unexpected recovered event counts: %#v", eventCounts)
	}
	after, err := store.LoadJSONLContent(chatID)
	if err != nil {
		t.Fatalf("load raw JSONL after replay: %v", err)
	}
	if after != before {
		t.Fatal("logical recovery rewrote the source JSONL")
	}
	if strings.Contains(after, `"react-tool"`) || strings.Contains(after, `"run_interrupted"`) {
		t.Fatalf("synthetic recovery leaked into raw JSONL: %s", after)
	}
}

func TestLegacyCancelledBatchAwaitingSynthesizesOneResultPerToolInCallOrder(t *testing.T) {
	const runID = "run-legacy-cancelled-batch"
	toolCalls := make([]any, 0, 2)
	approvals := make([]any, 0, 2)
	for _, toolID := range []string{"call-first", "call-second"} {
		toolCalls = append(toolCalls, map[string]any{
			"id":   toolID,
			"type": "function",
			"function": map[string]any{
				"name":      "file_write",
				"arguments": `{"path":"notes.txt"}`,
			},
		})
		approvals = append(approvals, map[string]any{"id": toolID})
	}
	lines := []map[string]any{{
		"chatId":    "chat-batch",
		"runId":     runID,
		"updatedAt": testEpochMillis(16),
		"_type":     StepLineTypeReact,
		"seq":       3,
		"messages": []any{map[string]any{
			"role":       "assistant",
			"tool_calls": toolCalls,
		}},
		"awaiting": []any{map[string]any{
			"type":       "awaiting.ask",
			"awaitingId": "await-batch",
			"mode":       "approval",
			"timestamp":  testEpochMillis(16),
			"approvals":  approvals,
		}},
	}}

	logical, err := filterLegacyIncompleteModelTurnsWithRuns(lines, map[string]legacyRepairableRunState{
		runID: {finishReason: "cancel", completedAt: testEpochMillis(17)},
	})
	if err != nil {
		t.Fatalf("recover cancelled batch: %v", err)
	}
	messages := anyMessageSlice(logical[2]["messages"])
	if len(messages) != 2 ||
		messages[0]["tool_call_id"] != "call-first" ||
		messages[1]["tool_call_id"] != "call-second" {
		t.Fatalf("batch results must follow original tool-call order: %#v", messages)
	}
}

func TestLegacyCancelledAwaitingRejectsPartiallyExecutedBatch(t *testing.T) {
	const runID = "run-legacy-partial-batch"
	react := map[string]any{
		"chatId":    "chat-partial-batch",
		"runId":     runID,
		"updatedAt": testEpochMillis(17),
		"_type":     StepLineTypeReact,
		"seq":       4,
		"messages": []any{map[string]any{
			"role": "assistant",
			"tool_calls": []any{
				map[string]any{"id": "call-finished", "type": "function", "function": map[string]any{"name": "file_read", "arguments": `{}`}},
				map[string]any{"id": "call-missing", "type": "function", "function": map[string]any{"name": "file_write", "arguments": `{}`}},
			},
		}},
		"awaiting": []any{map[string]any{
			"type":       "awaiting.ask",
			"awaitingId": "await-partial",
			"mode":       "approval",
			"timestamp":  testEpochMillis(17),
			"approvals":  []any{map[string]any{"id": "call-missing"}},
		}},
	}
	result := map[string]any{
		"runId":     runID,
		"updatedAt": testEpochMillis(18),
		"_type":     StepLineTypeReactTool,
		"seq":       4,
		"messages": []any{map[string]any{
			"role":         "tool",
			"tool_call_id": "call-finished",
			"content":      "already executed",
		}},
	}
	if _, err := filterLegacyIncompleteModelTurnsWithRuns(
		[]map[string]any{react, result},
		map[string]legacyRepairableRunState{runID: {finishReason: "cancel", completedAt: testEpochMillis(19)}},
	); !errors.Is(err, ErrChatHistoryIncomplete) {
		t.Fatalf("partially executed batch must remain incomplete, got %v", err)
	}
}

func TestLegacyCancelledRunRejectsConflictingSubmitOrUnmappedAwaiting(t *testing.T) {
	const runID = "run-legacy-cancelled-conflict"
	react := map[string]any{
		"chatId":    "chat-conflict",
		"runId":     runID,
		"updatedAt": testEpochMillis(18),
		"_type":     StepLineTypeReact,
		"seq":       1,
		"messages": []any{map[string]any{
			"role": "assistant",
			"tool_calls": []any{map[string]any{
				"id":   "call-one",
				"type": "function",
				"function": map[string]any{
					"name":      "bash",
					"arguments": `{}`,
				},
			}},
		}},
		"awaiting": []any{map[string]any{
			"type":       "awaiting.ask",
			"awaitingId": "await-one",
			"mode":       "approval",
			"approvals":  []any{map[string]any{"id": "call-one"}},
		}},
	}
	submit := map[string]any{
		"runId": runID,
		"_type": "submit",
		"answer": map[string]any{
			"awaitingId": "await-one",
			"status":     "ok",
		},
	}
	if _, err := filterLegacyIncompleteModelTurnsWithRuns(
		[]map[string]any{react, submit},
		map[string]legacyRepairableRunState{runID: {finishReason: "cancel", completedAt: testEpochMillis(19)}},
	); !errors.Is(err, ErrChatHistoryIncomplete) {
		t.Fatalf("conflicting submit must block logical recovery, got %v", err)
	}

	delete(react, "awaiting")
	if _, err := filterLegacyIncompleteModelTurnsWithRuns(
		[]map[string]any{react},
		map[string]legacyRepairableRunState{runID: {finishReason: "cancel", completedAt: testEpochMillis(19)}},
	); !errors.Is(err, ErrChatHistoryIncomplete) {
		t.Fatalf("missing awaiting evidence must block logical recovery, got %v", err)
	}
}

func TestStepWriterPersistsInterruptedAwaitingBeforeFlatToolResult(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	defer store.Close()
	const (
		chatID     = "chat-interrupted-persistence"
		runID      = "run-interrupted-persistence"
		toolID     = "call-interrupted"
		awaitingID = "await-batch-interrupted"
	)
	if _, _, err := store.EnsureChat(chatID, "agent", "", "run it"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	writer := NewStepWriter(store, chatID, runID, "REACT")
	dispatcher := stream.NewDispatcher(stream.StreamRequest{ChatID: chatID, RunID: runID})
	writeEvents := func(events []stream.StreamEvent) {
		t.Helper()
		for _, event := range events {
			writer.OnEvent(event.Data())
		}
	}

	writeEvents(dispatcher.Dispatch(stream.ToolArgs{
		ToolID:     toolID,
		ToolName:   "bash",
		Delta:      `{"command":"touch marker"}`,
		ChunkIndex: 0,
	}))
	writeEvents(dispatcher.Dispatch(stream.ToolEnd{ToolID: toolID}))
	writer.CommitModelTurn("", 2)
	writeEvents(dispatcher.Dispatch(stream.AwaitAsk{
		AwaitingID: awaitingID,
		Mode:       "approval",
		RunID:      runID,
		Timeout:    600,
		Approvals:  []any{map[string]any{"id": toolID}},
	}))
	writeEvents(dispatcher.Dispatch(stream.AwaitingAnswer{
		AwaitingID: awaitingID,
		Answer: map[string]any{
			"mode":   "approval",
			"status": "error",
			"error": map[string]any{
				"code":    "run_interrupted",
				"message": "run interrupted while waiting for user approval",
				"reason":  "run_interrupted",
			},
		},
	}))
	payload := map[string]any{
		"error":      "run_interrupted",
		"exitCode":   -1,
		"output":     "tool execution was cancelled before approval",
		"executed":   false,
		"awaitingId": awaitingID,
	}
	writeEvents(dispatcher.Dispatch(stream.ToolResult{
		ToolID:   toolID,
		ToolName: "bash",
		Result:   payload,
		Error:    "run_interrupted",
		ExitCode: -1,
	}))
	writeEvents(dispatcher.Dispatch(stream.RunCancel{RunID: runID}))
	writer.Flush()
	if err := writer.Err(); err != nil {
		t.Fatalf("persist interrupted result: %v", err)
	}

	lines, err := readJSONLines(store.chatJSONLPath(chatID))
	if err != nil {
		t.Fatalf("read persisted JSONL: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("expected react, submit, react-tool and no physical run.cancel, got %#v", lines)
	}
	if lines[0]["_type"] != StepLineTypeReact ||
		lines[1]["_type"] != "submit" ||
		lines[2]["_type"] != StepLineTypeReactTool {
		t.Fatalf("unexpected physical JSONL order: %#v", lines)
	}
	if toIntFromKeys(lines[0], "seq") <= 0 ||
		toIntFromKeys(lines[2], "seq") != toIntFromKeys(lines[0], "seq") {
		t.Fatalf("react-tool must reuse react seq: %#v", lines)
	}
	answer := mapValue(lines[1]["answer"])
	if answer["awaitingId"] != awaitingID ||
		answer["status"] != "error" ||
		mapValue(answer["error"])["code"] != "run_interrupted" {
		t.Fatalf("unexpected physical submit answer: %#v", lines[1])
	}
	if _, exists := answer["durationMs"]; !exists {
		t.Fatalf("interrupted answer must persist durationMs: %#v", answer)
	}
	if _, exists := answer["type"]; exists {
		t.Fatalf("interrupted answer must use the compact physical shape without type: %#v", answer)
	}
	if _, exists := answer["timestamp"]; exists {
		t.Fatalf("interrupted answer must inherit the submit line updatedAt: %#v", answer)
	}
	messages := anyMessageSlice(lines[2]["messages"])
	if len(messages) != 1 || messages[0]["role"] != "tool" {
		t.Fatalf("interrupted result must not add a fake user approval message: %#v", messages)
	}
	content := anyMessageSlice(messages[0]["content"])
	if len(content) != 1 {
		t.Fatalf("unexpected tool result content: %#v", messages[0]["content"])
	}
	var persistedPayload map[string]any
	if err := json.Unmarshal([]byte(stringValue(content[0]["text"])), &persistedPayload); err != nil {
		t.Fatalf("decode persisted payload: %v", err)
	}
	if persistedPayload["output"] != "tool execution was cancelled before approval" ||
		persistedPayload["executed"] != false ||
		persistedPayload["error"] != "run_interrupted" ||
		persistedPayload["exitCode"] != float64(-1) ||
		persistedPayload["awaitingId"] != awaitingID {
		t.Fatalf("unexpected flat persisted payload: %#v", persistedPayload)
	}
}

func TestLegacyRepairOnlyInspectsLiteralLastReact(t *testing.T) {
	lines := []map[string]any{
		{
			"_type": StepLineTypeReact,
			"runId": "run-failed",
			"seq":   1,
			"messages": []any{map[string]any{
				"role": "assistant",
				"tool_calls": []any{map[string]any{
					"id":   "call-truncated",
					"type": "function",
					"function": map[string]any{
						"name":      "file_write",
						"arguments": `{"path":"cut`,
					},
				}},
			}},
		},
		{
			"_type": StepLineTypeReact,
			"runId": "run-failed",
			"seq":   2,
			"messages": []any{map[string]any{
				"role":    "assistant",
				"content": "later complete boundary",
			}},
		},
	}

	filtered, err := filterLegacyIncompleteModelTurns(lines, map[string]bool{"run-failed": true})
	if err != nil {
		t.Fatalf("filter legacy history: %v", err)
	}
	if len(filtered) != len(lines) {
		t.Fatalf("only the literal last react may be repaired, got %#v", filtered)
	}
}

func TestLegacyRepairRejectsMalformedCallWithPersistedResult(t *testing.T) {
	lines := []map[string]any{
		{
			"_type": StepLineTypeReact,
			"runId": "run-failed",
			"seq":   1,
			"messages": []any{map[string]any{
				"role": "assistant",
				"tool_calls": []any{map[string]any{
					"id":   "call-malformed",
					"type": "function",
					"function": map[string]any{
						"name":      "file_write",
						"arguments": `{"path":"cut`,
					},
				}},
			}},
		},
		{
			"_type": StepLineTypeReactTool,
			"runId": "run-failed",
			"seq":   1,
			"messages": []any{map[string]any{
				"role":         "tool",
				"tool_call_id": "call-malformed",
				"content":      "executed",
			}},
		},
	}

	if _, err := filterLegacyIncompleteModelTurns(lines, map[string]bool{"run-failed": true}); !errors.Is(err, ErrChatHistoryIncomplete) {
		t.Fatalf("malformed executed call must be blocked, got %v", err)
	}
}

func TestLegacyRepairTreatsEmptyArgumentsAsStructurallyComplete(t *testing.T) {
	lines := []map[string]any{{
		"_type": StepLineTypeReact,
		"runId": "run-failed",
		"seq":   1,
		"messages": []any{map[string]any{
			"role": "assistant",
			"tool_calls": []any{map[string]any{
				"id":   "call-empty-args",
				"type": "function",
				"function": map[string]any{
					"name":      "datetime",
					"arguments": "",
				},
			}},
		}},
	}}

	if _, err := filterLegacyIncompleteModelTurns(lines, map[string]bool{"run-failed": true}); !errors.Is(err, ErrChatHistoryIncomplete) {
		t.Fatalf("empty arguments are valid and unmatched execution is ambiguous, got %v", err)
	}
}

func TestLegacyRepairRejectsTerminalToolTurnWithoutSequenceBoundary(t *testing.T) {
	lines := []map[string]any{{
		"_type": StepLineTypeReact,
		"runId": "run-failed",
		"messages": []any{map[string]any{
			"role": "assistant",
			"tool_calls": []any{map[string]any{
				"id":   "call-no-seq",
				"type": "function",
				"function": map[string]any{
					"name":      "file_write",
					"arguments": `{"path":"cut`,
				},
			}},
		}},
	}}

	if _, err := filterLegacyIncompleteModelTurns(lines, map[string]bool{"run-failed": true}); !errors.Is(err, ErrChatHistoryIncomplete) {
		t.Fatalf("missing seq boundary must be blocked, got %v", err)
	}
}

func TestStepWriterRetriesWithoutPersistingDiscardedAttempt(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	defer store.Close()
	const chatID = "chat-model-turn-retry"
	const runID = "run-model-turn-retry"
	if _, _, err := store.EnsureChat(chatID, "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := ensureRunStartedForTest(store, chatID, runID, testEpochMillis(20)); err != nil {
		t.Fatalf("start run: %v", err)
	}
	writer := NewStepWriter(store, chatID, runID, "REACT")

	writer.OnEvent(stream.NewEvent("llm.request", map[string]any{"runId": runID, "chatId": chatID}).Data())
	writer.OnEvent(stream.NewEvent("content.snapshot", map[string]any{"contentId": "attempt-1", "text": "partial"}).Data())
	writer.DiscardModelTurn("", 1, true)
	writer.OnEvent(stream.NewEvent("content.snapshot", map[string]any{"contentId": "attempt-2", "text": "complete"}).Data())
	writer.CommitModelTurn("", 1)
	writer.Flush()

	lines, err := readJSONLines(store.chatJSONLPath(chatID))
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	if len(lines) != 1 || toIntFromKeys(lines[0], "seq") != 1 {
		t.Fatalf("expected one committed react with seq=1, got %#v", lines)
	}
	data := string(mustJSONMarshalForTest(t, lines[0]))
	if strings.Contains(data, "partial") || !strings.Contains(data, "complete") {
		t.Fatalf("unexpected committed JSONL: %s", data)
	}
}

func TestStepWriterRequiresCommitForReplacementAfterTerminalDiscard(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	defer store.Close()
	const chatID = "chat-model-turn-replacement"
	const runID = "run-model-turn-replacement"
	if _, _, err := store.EnsureChat(chatID, "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := ensureRunStartedForTest(store, chatID, runID, testEpochMillis(30)); err != nil {
		t.Fatalf("start run: %v", err)
	}
	writer := NewStepWriter(store, chatID, runID, "REACT")

	writer.OnEvent(stream.NewEvent("llm.request", map[string]any{"runId": runID, "chatId": chatID}).Data())
	writer.OnEvent(stream.NewEvent("content.snapshot", map[string]any{"contentId": "discarded", "text": "partial"}).Data())
	writer.DiscardModelTurn("", 1, false)
	writer.OnEvent(stream.NewEvent("content.snapshot", map[string]any{"contentId": "replacement", "text": "safe replacement"}).Data())
	writer.CommitModelTurn("", 1)
	writer.Flush()

	lines, err := readJSONLines(store.chatJSONLPath(chatID))
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	if len(lines) != 1 || toIntFromKeys(lines[0], "seq") != 1 {
		t.Fatalf("expected one committed replacement with seq=1, got %#v", lines)
	}
	data := string(mustJSONMarshalForTest(t, lines[0]))
	if strings.Contains(data, "partial") || !strings.Contains(data, "safe replacement") {
		t.Fatalf("unexpected committed JSONL: %s", data)
	}
}

func TestStepWriterTerminalDiscardDoesNotPersistReact(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	defer store.Close()
	const chatID = "chat-model-turn-exhausted"
	const runID = "run-model-turn-exhausted"
	if _, _, err := store.EnsureChat(chatID, "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := ensureRunStartedForTest(store, chatID, runID, testEpochMillis(40)); err != nil {
		t.Fatalf("start run: %v", err)
	}
	writer := NewStepWriter(store, chatID, runID, "REACT")

	writer.OnEvent(stream.NewEvent("llm.request", map[string]any{"runId": runID, "chatId": chatID}).Data())
	writer.OnEvent(stream.NewEvent("content.snapshot", map[string]any{"contentId": "failed", "text": "partial"}).Data())
	writer.DiscardModelTurn("", 1, false)
	writer.OnEvent(stream.NewEvent("run.error", map[string]any{"runId": runID, "error": map[string]any{"message": "exhausted"}}).Data())
	writer.Flush()

	lines, err := readJSONLines(store.chatJSONLPath(chatID))
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	if len(lines) != 1 || stringFromAny(lines[0]["_type"]) != "event" {
		t.Fatalf("terminal discard must persist only the run.error event: %#v", lines)
	}
	event, _ := lines[0]["event"].(map[string]any)
	if stringFromAny(event["type"]) != "run.error" {
		t.Fatalf("expected persisted run.error, got %#v", lines[0])
	}
}

func mustJSONMarshalForTest(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal test value: %v", err)
	}
	return data
}
