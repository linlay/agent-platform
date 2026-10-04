package server

import (
	"strings"
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/runtime/runexec"
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
	consume("tool.snapshot", map[string]any{"toolId": "desktop-bad", "toolName": "desktop_action", "arguments": map[string]any{"partial": true}})
	consume("content.delta", map[string]any{"delta": "partial"})
	consume("run.activity", map[string]any{
		"status": "retrying",
		"recovery": map[string]any{
			"action":       "discard_incomplete_model_turn",
			"reasoningIds": []string{"reasoning-bad"},
			"toolIds":      []string{"tool-bad", "desktop-bad"},
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

type queryEventCollector struct {
	AssistantText   strings.Builder
	ModelTurnText   strings.Builder
	ModelTurnOpen   bool
	ModelTurnDirty  bool
	FinishReason    string
	Usage           chat.UsageData
	FullTextBuilder *queryFullTextBuilder
	ErrorMessage    string
	ErrorPayload    map[string]any
}

func newQueryEventCollector(includeFullText bool) *queryEventCollector {
	c := &queryEventCollector{}
	if includeFullText {
		c.FullTextBuilder = newQueryFullTextBuilder()
	}
	return c
}

func (c *queryEventCollector) Consume(event stream.EventData) {
	if c == nil {
		return
	}
	if event.Type == "run.activity" && isDiscardIncompleteModelTurnRecovery(event.Value("recovery")) {
		c.ModelTurnText.Reset()
		c.ModelTurnOpen = true
		c.ModelTurnDirty = false
		if c.FullTextBuilder != nil {
			c.FullTextBuilder.DiscardModelTurn(event.Value("recovery"))
		}
		return
	}
	if c.FullTextBuilder != nil {
		c.FullTextBuilder.Consume(event)
	}
	switch event.Type {
	case "llm.request":
		c.FinishModelTurn()
		c.ModelTurnOpen = true
		c.ModelTurnDirty = false
	case "content.delta":
		if delta := event.String("delta"); delta != "" {
			c.ModelTurnDirty = c.ModelTurnOpen
			c.ContentBuffer().WriteString(delta)
		}
	case "content.snapshot":
		if text := event.String("text"); text != "" {
			c.ModelTurnDirty = c.ModelTurnOpen
			buffer := c.ContentBuffer()
			buffer.Reset()
			buffer.WriteString(text)
		}
	case "content.end":
		if text := event.String("text"); text != "" && c.ContentBuffer().Len() == 0 {
			c.ModelTurnDirty = c.ModelTurnOpen
			c.ContentBuffer().WriteString(text)
		}
	case "usage.snapshot":
		c.ConsumeUsage(event)
	case "run.complete":
		c.FinishModelTurn()
		c.FinishReason = "complete"
		c.ConsumeUsage(event)
	case "run.cancel":
		c.FinishModelTurn()
		c.FinishReason = "cancel"
		c.ConsumeUsage(event)
	case "run.error":
		c.FinishModelTurn()
		c.FinishReason = "error"
		if message := queryEventErrorMessage(event); message != "" {
			c.ErrorMessage = message
		}
		if payload := queryEventErrorPayload(event); len(payload) > 0 {
			c.ErrorPayload = payload
		}
		c.ConsumeUsage(event)
	}
}

func (c *queryEventCollector) ContentBuffer() *strings.Builder {
	if c.ModelTurnOpen {
		return &c.ModelTurnText
	}
	return &c.AssistantText
}

func (c *queryEventCollector) FinishModelTurn() {
	if !c.ModelTurnOpen {
		return
	}
	if c.ModelTurnDirty {
		c.AssistantText.Reset()
		c.AssistantText.WriteString(c.ModelTurnText.String())
	}
	c.ModelTurnText.Reset()
	c.ModelTurnOpen = false
	c.ModelTurnDirty = false
}

func (c *queryEventCollector) Result() queryRunResult {
	if c == nil {
		return queryRunResult{FinishReason: "complete"}
	}
	finishReason := strings.TrimSpace(c.FinishReason)
	if finishReason == "" {
		finishReason = "complete"
	}
	assistantText := c.AssistantText.String()
	if c.ModelTurnOpen && c.ModelTurnDirty {
		assistantText = c.ModelTurnText.String()
	}
	return queryRunResult{
		AssistantText: assistantText,
		FinishReason:  finishReason,
		Usage:         c.Usage,
		FullText:      c.FullText(assistantText),
		ErrorMessage:  c.ErrorMessage,
		ErrorPayload:  cloneQueryErrorPayload(c.ErrorPayload),
	}
}

func (c *queryEventCollector) FullText(content string) string {
	if c == nil || c.FullTextBuilder == nil {
		return ""
	}
	return c.FullTextBuilder.Text(content)
}

// Observe retains internal recovery signals; public EventBus events alone
// cannot reconstruct discarded model attempts for a blocking fullText result.

func (c *queryEventCollector) ConsumeUsage(event stream.EventData) {
	if c == nil || event.Payload == nil {
		return
	}
	usage, _ := event.Payload["usage"].(map[string]any)
	if usage == nil {
		return
	}
	if run, _ := usage["run"].(map[string]any); run != nil {
		runexec.MergeUsageMapIntoRunData(&c.Usage, run)
		return
	}
	runexec.MergeUsageMapIntoRunData(&c.Usage, usage)
}
