package chat

import "testing"

func TestWaitProgressPreservesOriginalToolGroup(t *testing.T) {
	ask := func(progress int) []any {
		return []any{map[string]any{"type": "awaiting.ask", "awaitingId": "wait-1", "mode": "wait", "progress": progress}}
	}
	lines := []map[string]any{
		{"_type": "react", "runId": "run", "seq": 1, "awaiting": ask(0), "messages": []any{map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "wait-1", "function": map[string]any{"name": "wait"}}, map[string]any{"id": "later", "function": map[string]any{"name": "file_read"}}}}}},
		{"_type": "react", "runId": "run", "seq": 2, "awaiting": ask(1)},
		{"_type": "react-tool", "runId": "run", "seq": 1, "messages": []any{map[string]any{"role": "tool", "tool_call_id": "wait-1", "content": "done"}}},
	}
	step := loadPersistedAwaitingStepFromLines(lines, "wait-1")
	if step == nil || step.Seq != 1 || len(step.ToolCalls) != 2 || step.Ask.Payload["progress"] != 1 || !step.ResultToolIDs["wait-1"] {
		t.Fatalf("lost original tool group or updated progress: %+v", step)
	}
}
