package chat

import (
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

func TestTeamCoordinatorHistoryKeepsFinalMemberBodiesWithoutHiddenChains(t *testing.T) {
	lines := []map[string]any{
		{"_type": "query", "runId": "run-1", "updatedAt": float64(1), "messages": []any{map[string]any{"role": "user", "content": "continue the draft"}}},
		{"_type": StepLineTypeReact, "runId": "run-1", "messages": []any{
			map[string]any{"role": "assistant", "reasoning_content": "private routing thought", "tool_calls": []any{
				map[string]any{"id": "delegate", "function": map[string]any{"name": "agent_delegate", "arguments": `{"tasks":[{"agentKey":"editor","task":"private edit"}]}`}},
				map[string]any{"id": "route", "function": map[string]any{"name": "team_delegate", "arguments": `{"mode":"direct","memberKey":"writer"}`}},
				map[string]any{"id": "invoke", "function": map[string]any{"name": "team_invoke", "arguments": `{"tasks":[{"memberKey":"reviewer","task":"secret internal instruction"}]}`}},
			}},
		}},
		{"_type": StepLineTypeReact, "runId": "run-1", "taskSubAgentKey": "writer", "messages": []any{
			map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "bash", "function": map[string]any{"name": "bash"}}}},
			map[string]any{"role": "tool", "tool_call_id": "bash", "content": "private file contents"},
			map[string]any{"role": "assistant", "content": "writer final answer"},
		}},
		{"_type": StepLineTypeReact, "runId": "run-2", "messages": []any{
			map[string]any{"role": "assistant", "actorType": "agent", "agentKey": "editor", "teamId": "research", "presentation": "reply", "content": "direct editor answer"},
		}},
	}

	messages := teamCoordinatorRawMessagesFromJSONLLines(lines)
	serialized := ""
	for _, message := range messages {
		serialized += stringValue(message["content"]) + "\n"
	}
	for _, required := range []string{"continue the draft", "[Team routing record]\nagent_delegate agentKeys=editor\nagent_delegate agentKeys=writer\nagent_delegate agentKeys=reviewer", "[Team member writer]\nwriter final answer", "[Team member editor]\ndirect editor answer"} {
		if !strings.Contains(serialized, required) {
			t.Fatalf("coordinator history missing %q: %#v", required, messages)
		}
	}
	for _, forbidden := range []string{"private routing thought", "private edit", "secret internal instruction", "private file contents", "bash", "team_delegate", "team_invoke"} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("coordinator history leaked %q: %#v", forbidden, messages)
		}
	}
}
