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
