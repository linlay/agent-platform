package chat

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRawMessagesExcludeSubAgentQueryPrompts(t *testing.T) {
	lines := []map[string]any{
		{
			"_type":     "query",
			"runId":     "run-1",
			"updatedAt": float64(1),
			"messages": []any{map[string]any{
				"role": "user", "content": "visible root prompt",
			}},
		},
		{
			"_type":       "query",
			"runId":       "run-1",
			"taskId":      "task-1",
			"subAgentKey": "writer",
			"updatedAt":   float64(2),
			"messages": []any{map[string]any{
				"role": "user", "content": "hidden orchestration prompt",
			}},
		},
	}

	messages := rawMessagesFromJSONLLines(lines)
	if len(messages) != 1 {
		t.Fatalf("messages len = %d, want 1: %#v", len(messages), messages)
	}
	if got, _ := messages[0]["content"].(string); got != "visible root prompt" {
		t.Fatalf("content = %q, want visible root prompt", got)
	}
}

func TestTeamMemberHistoryKeepsOwnChainAndOnlyOtherFinalBodies(t *testing.T) {
	lines := []map[string]any{
		{"_type": "query", "runId": "run-1", "updatedAt": float64(1), "messages": []any{map[string]any{"role": "user", "content": "user request"}}},
		{"_type": StepLineTypeReact, "runId": "run-1", "messages": []any{
			map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "route", "function": map[string]any{"name": "team_delegate"}}}},
			map[string]any{"role": "tool", "tool_call_id": "route", "content": "private coordinator result"},
			map[string]any{"role": "assistant", "content": "Team summary"},
		}},
		{"_type": StepLineTypeReact, "runId": "run-1", "taskSubAgentKey": "writer", "messages": []any{
			map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "bash", "function": map[string]any{"name": "bash"}}}},
			map[string]any{"role": "tool", "tool_call_id": "bash", "content": "writer private tool result"},
			map[string]any{"role": "assistant", "content": "writer final"},
		}},
		{"_type": StepLineTypeReact, "runId": "run-1", "taskSubAgentKey": "reviewer", "messages": []any{
			map[string]any{"role": "assistant", "reasoning_content": "reviewer private thought", "content": "reviewer final"},
		}},
	}

	messages := teamMemberRawMessagesFromJSONLLines(lines, "writer")
	serialized := ""
	for _, message := range messages {
		if content, _ := message["content"].(string); content != "" {
			serialized += content + "\n"
		}
	}
	for _, required := range []string{"user request", "Team summary", "writer private tool result", "writer final", "[Team member reviewer]\nreviewer final"} {
		if !strings.Contains(serialized, required) {
			t.Fatalf("history missing %q: %#v", required, messages)
		}
	}
	for _, forbidden := range []string{"private coordinator result", "reviewer private thought"} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("history leaked %q: %#v", forbidden, messages)
		}
	}
}

func TestTeamCoordinatorHistoryPreservesOwnToolPairsAndMemberFinals(t *testing.T) {
	lines := []map[string]any{
		{"_type": "query", "runId": "run-1", "messages": []any{map[string]any{"role": "user", "content": "request"}}},
		{"_type": StepLineTypeReact, "runId": "run-1", "messages": []any{
			map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "read", "function": map[string]any{"name": "file_read", "arguments": "{}"}}}},
			map[string]any{"role": "tool", "tool_call_id": "read", "content": "coordinator private result"},
			map[string]any{"role": "assistant", "content": "root final"},
		}},
		{"_type": StepLineTypeReact, "runId": "run-1", "taskSubAgentKey": "writer", "messages": []any{
			map[string]any{"role": "tool", "tool_call_id": "member-read", "content": "member private result"},
			map[string]any{"role": "assistant", "content": "member final"},
		}},
	}
	messages := teamCoordinatorRawMessagesFromJSONLLines(lines)
	data, _ := json.Marshal(messages)
	text := string(data)
	for _, want := range []string{"coordinator private result", "tool_calls", "tool_call_id", "root final", "member final"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s: %s", want, text)
		}
	}
	if strings.Contains(text, "member private result") {
		t.Fatalf("member private chain leaked: %s", text)
	}
	member, _ := json.Marshal(teamMemberRawMessagesFromJSONLLines(lines, "writer"))
	if strings.Contains(string(member), "coordinator private result") || !strings.Contains(string(member), "member private result") {
		t.Fatalf("member history: %s", member)
	}
}
