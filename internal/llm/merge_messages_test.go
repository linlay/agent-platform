package llm

import (
	"testing"
)

func TestMergeRawMessagesByMsgID_ParallelToolCalls(t *testing.T) {
	raw := []map[string]any{
		{"role": "assistant", "content": "\n", "_msgId": "m_1"},
		{"role": "assistant", "tool_calls": []any{map[string]any{"id": "t1", "type": "function", "function": map[string]any{"name": "bash", "arguments": `{"cmd":"ls"}`}}}, "_msgId": "m_1"},
		{"role": "assistant", "tool_calls": []any{map[string]any{"id": "t2", "type": "function", "function": map[string]any{"name": "bash", "arguments": `{"cmd":"pwd"}`}}}, "_msgId": "m_1"},
		{"role": "tool", "tool_call_id": "t1", "name": "bash", "content": "file1"},
		{"role": "tool", "tool_call_id": "t2", "name": "bash", "content": "file2"},
	}

	result := mergeRawMessagesByMsgID(raw)

	// Expect: 1 assistant + 2 tool = 3 messages
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}

	assistant := result[0]
	if assistant["role"] != "assistant" {
		t.Fatalf("expected assistant role, got %s", assistant["role"])
	}
	calls, ok := assistant["tool_calls"].([]any)
	if !ok || len(calls) != 2 {
		t.Fatalf("expected 2 tool_calls in merged assistant, got %v", assistant["tool_calls"])
	}
	if result[1]["role"] != "tool" || result[2]["role"] != "tool" {
		t.Fatal("expected tool messages after assistant")
	}
}

func TestMergeRawMessagesByMsgID_ReasoningContentToolCalls(t *testing.T) {
	raw := []map[string]any{
		{"role": "assistant", "reasoning_content": "thinking...", "_msgId": "m_1"},
		{"role": "assistant", "content": "hello", "_msgId": "m_1"},
		{"role": "assistant", "tool_calls": []any{map[string]any{"id": "t1"}}, "_msgId": "m_1"},
		{"role": "tool", "tool_call_id": "t1", "content": "result"},
	}

	result := mergeRawMessagesByMsgID(raw)

	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	assistant := result[0]
	if assistant["content"] != "hello" {
		t.Errorf("expected content='hello', got %v", assistant["content"])
	}
	if assistant["reasoning_content"] != "thinking..." {
		t.Errorf("expected reasoning_content='thinking...', got %v", assistant["reasoning_content"])
	}
	calls, _ := assistant["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Errorf("expected 1 tool_call, got %d", len(calls))
	}
}

func TestMergeRawMessagesByMsgID_EmptyAssistantDropped(t *testing.T) {
	raw := []map[string]any{
		{"role": "assistant", "reasoning_content": "thinking...", "_msgId": "m_1"},
		{"role": "user", "content": "hi"},
	}

	result := mergeRawMessagesByMsgID(raw)

	// reasoning-only assistant should be dropped (no content, no tool_calls)
	if len(result) != 1 {
		t.Fatalf("expected 1 message (only user), got %d", len(result))
	}
	if result[0]["role"] != "user" {
		t.Errorf("expected user message, got %v", result[0]["role"])
	}
}

func TestMergeRawMessagesByMsgID_ToolResultsNotMerged(t *testing.T) {
	raw := []map[string]any{
		{"role": "tool", "tool_call_id": "t1", "content": "r1"},
		{"role": "tool", "tool_call_id": "t2", "content": "r2"},
	}

	result := mergeRawMessagesByMsgID(raw)

	if len(result) != 2 {
		t.Fatalf("expected 2 tool messages, got %d", len(result))
	}
}

func TestMergeRawMessagesByMsgID_NoMsgIDPassthrough(t *testing.T) {
	raw := []map[string]any{
		{"role": "assistant", "content": "no id"},
		{"role": "assistant", "content": "also no id"},
	}

	result := mergeRawMessagesByMsgID(raw)

	if len(result) != 2 {
		t.Fatalf("expected 2 messages (no merging without _msgId), got %d", len(result))
	}
}

func TestMergeRawMessagesByMsgID_DifferentMsgIDs(t *testing.T) {
	raw := []map[string]any{
		{"role": "assistant", "content": "a", "_msgId": "m_1"},
		{"role": "assistant", "tool_calls": []any{map[string]any{"id": "t1"}}, "_msgId": "m_1"},
		{"role": "tool", "tool_call_id": "t1", "content": "r1"},
		{"role": "assistant", "content": "b", "_msgId": "m_2"},
		{"role": "assistant", "tool_calls": []any{map[string]any{"id": "t2"}}, "_msgId": "m_2"},
		{"role": "tool", "tool_call_id": "t2", "content": "r2"},
	}

	result := mergeRawMessagesByMsgID(raw)

	// 2 merged assistants + 2 tools = 4
	if len(result) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(result))
	}
	if result[0]["role"] != "assistant" || result[0]["content"] != "a" {
		t.Errorf("first assistant wrong: %v", result[0])
	}
	if result[1]["role"] != "tool" {
		t.Errorf("expected tool after first assistant")
	}
	if result[2]["role"] != "assistant" || result[2]["content"] != "b" {
		t.Errorf("second assistant wrong: %v", result[2])
	}
	if result[3]["role"] != "tool" {
		t.Errorf("expected tool after second assistant")
	}
}

func TestMergeRawMessagesByMsgID_FullBugScenario(t *testing.T) {
	// Reproduces the exact sequence from the bug report:
	// reasoning + content + 4 parallel tool_calls + 4 tool_results
	raw := []map[string]any{
		// Turn 1: reasoning
		{"role": "assistant", "reasoning_content": "checking git config...", "_msgId": "m_de287661", "_reasoningId": "r_1"},
		// Turn 1: content
		{"role": "assistant", "content": "\n", "_msgId": "m_de287661", "_contentId": "c_1"},
		// Turn 1: 4 parallel tool_calls
		{"role": "assistant", "tool_calls": []any{map[string]any{"id": "call_1", "type": "function", "function": map[string]any{"name": "_bash_", "arguments": `{"command":"cd /a && git config --local user.name"}`}}}, "_msgId": "m_de287661", "_toolId": "call_1"},
		{"role": "assistant", "tool_calls": []any{map[string]any{"id": "call_2", "type": "function", "function": map[string]any{"name": "_bash_", "arguments": `{"command":"cd /b && git config --local user.name"}`}}}, "_msgId": "m_de287661", "_toolId": "call_2"},
		{"role": "assistant", "tool_calls": []any{map[string]any{"id": "call_3", "type": "function", "function": map[string]any{"name": "_bash_", "arguments": `{"command":"cd /c && git config --local user.name"}`}}}, "_msgId": "m_de287661", "_toolId": "call_3"},
		{"role": "assistant", "tool_calls": []any{map[string]any{"id": "call_4", "type": "function", "function": map[string]any{"name": "_bash_", "arguments": `{"command":"cd /d && git config --local user.name"}`}}}, "_msgId": "m_de287661", "_toolId": "call_4"},
		// 4 tool results
		{"role": "tool", "tool_call_id": "call_1", "name": "_bash_", "content": "exit:1"},
		{"role": "tool", "tool_call_id": "call_2", "name": "_bash_", "content": "exit:1"},
		{"role": "tool", "tool_call_id": "call_3", "name": "_bash_", "content": "linlay\nexit:0"},
		{"role": "tool", "tool_call_id": "call_4", "name": "_bash_", "content": "exit:1"},
	}

	result := mergeRawMessagesByMsgID(raw)

	// Expected: 1 merged assistant + 4 tool results = 5
	if len(result) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(result))
	}

	assistant := result[0]
	if assistant["role"] != "assistant" {
		t.Fatalf("expected assistant, got %s", assistant["role"])
	}
	calls, ok := assistant["tool_calls"].([]any)
	if !ok {
		t.Fatalf("expected tool_calls array, got %T", assistant["tool_calls"])
	}
	if len(calls) != 4 {
		t.Fatalf("expected 4 tool_calls, got %d", len(calls))
	}
	// Content should be "\n" (from content entry)
	if assistant["content"] != "\n" {
		t.Errorf("expected content='\\n', got %q", assistant["content"])
	}

	// Verify tool results are in order
	for i := 1; i <= 4; i++ {
		if result[i]["role"] != "tool" {
			t.Errorf("result[%d] should be tool, got %v", i, result[i]["role"])
		}
	}
}

func TestRawMessageToOpenAI_EmptyAssistantFiltered(t *testing.T) {
	// Empty content, no tool_calls → should return zero-value (empty Role)
	raw := map[string]any{
		"role":    "assistant",
		"content": "",
	}
	msg := rawMessageToOpenAI(raw)
	if msg.Role != "" {
		t.Errorf("expected empty Role for empty assistant, got %q", msg.Role)
	}

	// Whitespace-only content, no tool_calls → also filtered
	raw2 := map[string]any{
		"role":    "assistant",
		"content": "  \n  ",
	}
	msg2 := rawMessageToOpenAI(raw2)
	if msg2.Role != "" {
		t.Errorf("expected empty Role for whitespace-only assistant, got %q", msg2.Role)
	}

	// Content with tool_calls → NOT filtered
	raw3 := map[string]any{
		"role":       "assistant",
		"content":    "",
		"tool_calls": []any{map[string]any{"id": "t1", "type": "function", "function": map[string]any{"name": "bash", "arguments": "{}"}}},
	}
	msg3 := rawMessageToOpenAI(raw3)
	if msg3.Role != "assistant" {
		t.Errorf("expected assistant with tool_calls to pass, got Role=%q", msg3.Role)
	}
}
