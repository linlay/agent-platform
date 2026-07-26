package server

import (
	"strings"
	"testing"

	"agent-platform/internal/stream"
)

func TestQueryEventCollectorDiscardsOnlyIncompleteModelTurn(t *testing.T) {
	collector := newQueryEventCollector(true)
	consume := func(eventType string, payload map[string]any) {
		collector.Consume(stream.EventData{Type: eventType, Payload: payload})
	}

	consume("llm.request", nil)
	consume("content.delta", map[string]any{"delta": "accepted "})
	consume("llm.request", nil)
	consume("reasoning.snapshot", map[string]any{"reasoningId": "reasoning-bad", "text": "partial reasoning"})
	consume("tool.snapshot", map[string]any{"toolId": "tool-bad", "toolName": "file_write", "arguments": `{"path":"cut`})
	consume("action.snapshot", map[string]any{"actionId": "action-bad", "actionName": "desktop", "arguments": map[string]any{"partial": true}})
	consume("content.delta", map[string]any{"delta": "partial"})
	consume("run.activity", map[string]any{
		"status": "retrying",
		"recovery": map[string]any{
			"action":       "discard_incomplete_model_turn",
			"reasoningIds": []string{"reasoning-bad"},
			"toolIds":      []string{"tool-bad"},
			"actionIds":    []string{"action-bad"},
		},
	})
	consume("content.delta", map[string]any{"delta": "success"})
	consume("run.complete", nil)

	result := collector.Result()
	if result.AssistantText != "success" {
		t.Fatalf("assistant text = %q, want only the final successful model turn", result.AssistantText)
	}
	for _, discarded := range []string{"partial reasoning", "file_write", "desktop", "partial"} {
		if strings.Contains(result.FullText, discarded) {
			t.Fatalf("full text retained discarded value %q: %q", discarded, result.FullText)
		}
	}
	if !strings.Contains(result.FullText, "success") {
		t.Fatalf("full text missing successful answer: %q", result.FullText)
	}
}

func TestQueryEventCollectorAcceptsCommittedReplacementAfterDiscard(t *testing.T) {
	collector := newQueryEventCollector(false)
	consume := func(eventType string, payload map[string]any) {
		collector.Consume(stream.EventData{Type: eventType, Payload: payload})
	}

	consume("llm.request", nil)
	consume("content.delta", map[string]any{"delta": "partial"})
	consume("run.activity", map[string]any{
		"status": "discarded",
		"recovery": map[string]any{
			"action":     "discard_incomplete_model_turn",
			"contentIds": []string{"content-bad"},
		},
	})
	consume("content.delta", map[string]any{"delta": "safe replacement"})
	consume("run.complete", nil)

	if result := collector.Result(); result.AssistantText != "safe replacement" {
		t.Fatalf("assistant text = %q, want committed replacement", result.AssistantText)
	}
}

func TestQueryEventCollectorTerminalDiscardRetainsPriorCommittedSummary(t *testing.T) {
	collector := newQueryEventCollector(false)
	consume := func(eventType string, payload map[string]any) {
		collector.Consume(stream.EventData{Type: eventType, Payload: payload})
	}

	consume("llm.request", nil)
	consume("content.delta", map[string]any{"delta": "prior accepted"})
	consume("llm.request", nil)
	consume("content.delta", map[string]any{"delta": "partial"})
	consume("run.activity", map[string]any{
		"status": "discarded",
		"recovery": map[string]any{
			"action":     "discard_incomplete_model_turn",
			"contentIds": []string{"content-bad"},
		},
	})
	consume("run.error", map[string]any{"error": map[string]any{"message": "failed"}})

	if result := collector.Result(); result.AssistantText != "prior accepted" {
		t.Fatalf("assistant text = %q, want prior committed summary", result.AssistantText)
	}
}

func TestRunEventProcessorCommitsReplacementAfterDiscard(t *testing.T) {
	var summary strings.Builder
	processor := &runEventProcessor{assistantText: &summary}

	processor.beginModelTurn("")
	partial := stream.EventData{Type: "content.delta", Payload: map[string]any{"delta": "partial"}}
	processor.decorate(&partial)
	processor.discardModelTurn("", false)

	replacement := stream.EventData{Type: "content.delta", Payload: map[string]any{"delta": "safe replacement"}}
	processor.decorate(&replacement)
	processor.commitModelTurn("")

	if summary.String() != "safe replacement" {
		t.Fatalf("summary = %q, want committed replacement", summary.String())
	}
}
