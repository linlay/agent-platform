package stream

import (
	"strings"
	"testing"
)

func TestNormalizerHidesOnlyToolEventsForHiddenTool(t *testing.T) {
	normalizer := NewNormalizer()
	normalizer.RegisterHiddenTools("hidden_tool")

	events := []StreamEvent{
		NewEvent("tool.start", map[string]any{
			"toolId":   "tool_1",
			"toolName": "hidden_tool",
		}),
		NewEvent("awaiting.ask", map[string]any{
			"awaitingId": "tool_1",
			"mode":       "question",
		}),
		NewEvent("tool.args", map[string]any{
			"toolId": "tool_1",
			"delta":  "{}",
		}),
		NewEvent("tool.end", map[string]any{
			"toolId": "tool_1",
		}),
		NewEvent("request.submit", map[string]any{
			"awaitingId": "tool_1",
			"params":     []any{map[string]any{"answer": "approve"}},
		}),
		NewEvent("awaiting.answer", map[string]any{
			"awaitingId": "tool_1",
			"mode":       "approval",
			"value":      "approve",
		}),
		NewEvent("tool.result", map[string]any{
			"toolId": "tool_1",
			"result": "ok",
		}),
	}

	got := normalizer.Normalize(events)
	assertEventTypes(t, got, "awaiting.ask", "request.submit", "awaiting.answer")
}

func TestNormalizerKeepsCapabilityEventsFromHiddenTool(t *testing.T) {
	normalizer := NewNormalizer()
	normalizer.RegisterHiddenTools("hidden_tool")

	events := []StreamEvent{
		NewEvent("tool.start", map[string]any{
			"toolId":   "tool_1",
			"toolName": "hidden_tool",
		}),
		NewEvent("artifact.publish", map[string]any{
			"runId":         "run_1",
			"artifactCount": 1,
			"artifacts":     []any{map[string]any{"name": "report.md"}},
		}),
		NewEvent("memory.write", map[string]any{
			"runId": "run_1",
			"data":  map[string]any{"toolId": "tool_1"},
		}),
		NewEvent("tool.result", map[string]any{
			"toolId": "tool_1",
			"result": "ok",
		}),
	}

	got := normalizer.Normalize(events)
	assertEventTypes(t, got, "artifact.publish", "memory.write")
	for _, event := range got {
		if strings.HasPrefix(event.Type, "tool.") {
			t.Fatalf("did not expect tool event for hidden tool, got %#v", got)
		}
	}
}

func TestNormalizerKeepsPlanningDeltaFromHiddenPlanningWrite(t *testing.T) {
	normalizer := NewNormalizer()
	normalizer.RegisterHiddenTools("planning_write")

	events := []StreamEvent{
		NewEvent("tool.start", map[string]any{
			"toolId":   "tool_plan",
			"toolName": "planning_write",
		}),
		NewEvent("tool.args", map[string]any{
			"toolId": "tool_plan",
			"delta":  `{"markdown":"# Plan"}`,
		}),
		NewEvent("planning.delta", map[string]any{
			"planningId": "run_1_planning",
			"delta":      "# Plan",
		}),
		NewEvent("tool.end", map[string]any{
			"toolId": "tool_plan",
		}),
		NewEvent("tool.result", map[string]any{
			"toolId": "tool_plan",
			"result": "ok",
		}),
	}

	got := normalizer.Normalize(events)
	assertEventTypes(t, got, "planning.delta")
	for _, event := range got {
		if strings.HasPrefix(event.Type, "tool.") {
			t.Fatalf("did not expect tool event for hidden planning_write, got %#v", got)
		}
	}
}
