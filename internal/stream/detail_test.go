package stream

import "testing"

func TestParseDetailLevel(t *testing.T) {
	for raw, want := range map[string]DetailLevel{"": "", "summary": DetailSummary, "full": DetailFull, " full ": DetailFull} {
		if got, ok := ParseDetailLevel(raw); !ok || got != want {
			t.Fatalf("ParseDetailLevel(%q)=%q,%v want %q", raw, got, ok, want)
		}
	}
	for _, raw := range []string{"FULL", "detail", "debug", "none"} {
		if _, ok := ParseDetailLevel(raw); ok {
			t.Fatalf("ParseDetailLevel(%q) accepted", raw)
		}
	}
}

func TestDetailLevelActivities(t *testing.T) {
	progress := newActivityEvent("run", "chat", "", map[string]any{"kind": "thinking"}).Payload
	retry := newRetryActivityEvent("run", "chat", "", map[string]any{"attempt": 2}).Payload
	if DetailSummary.Allows("run.activity", progress) || DetailSummary.Allows("run.activity", retry) {
		t.Fatal("summary must hide automatic progress and retries")
	}
	// full is a superset: one full stream serves viewers of either level.
	if !DetailFull.Allows("run.activity", progress) || !DetailFull.Allows("run.activity", retry) {
		t.Fatal("full must receive every activity")
	}
}

func TestSummaryDetailHasAnnouncementsOnly(t *testing.T) {
	for eventType := range summaryEventTypes {
		if !DetailSummary.Allows(eventType, map[string]any{}) {
			t.Fatalf("summary dropped %s", eventType)
		}
	}
	for _, eventType := range []string{"tool.start", "reasoning.start", "usage.snapshot", "some.future.event"} {
		if DetailSummary.Allows(eventType, map[string]any{}) {
			t.Fatalf("summary leaked %s", eventType)
		}
	}
	for _, payload := range []map[string]any{
		newActivityEvent("run", "chat", "", map[string]any{"kind": ActivityKindAnnounce, "text": "Next."}).Payload,
		newActivityEvent("run", "chat", "member", map[string]any{"kind": ActivityKindAnnounce, "text": "Next."}).Payload,
		newActivityEvent("run", "chat", "", map[string]any{"kind": "idle"}).Payload,
	} {
		if !DetailSummary.Allows("run.activity", payload) {
			t.Fatalf("summary dropped %#v", payload)
		}
	}
	for _, payload := range []map[string]any{
		newActivityEvent("run", "chat", "", map[string]any{"kind": "thinking"}).Payload,
		newActivityEvent("run", "chat", "", map[string]any{"kind": "reasoning"}).Payload,
		newActivityEvent("run", "chat", "", map[string]any{"kind": "tool_run", "toolName": "bash"}).Payload,
		newActivityEvent("run", "chat", "member", map[string]any{"kind": "subtask"}).Payload,
		newActivityEvent("run", "chat", "", map[string]any{"kind": "awaiting"}).Payload,
		newActivityEvent("run", "chat", "", map[string]any{"kind": "compacting"}).Payload,
		{"kind": "future_activity"},
		newRetryActivityEvent("run", "chat", "", map[string]any{"attempt": 2}).Payload,
	} {
		if DetailSummary.Allows("run.activity", payload) {
			t.Fatalf("summary leaked activity %#v", payload)
		}
	}
	// Proxy activities may already be rendered and have only a top-level kind.
	for _, kind := range []string{ActivityKindAnnounce, "idle"} {
		if !DetailSummary.Allows("run.activity", map[string]any{"kind": kind}) {
			t.Fatalf("summary dropped rendered %s", kind)
		}
	}
}

func TestSummaryDetailKeepsOnlyReadableEvents(t *testing.T) {
	for _, eventType := range []string{
		"request.query", "request.steer", "request.submit", "chat.start",
		"run.start", "run.complete", "run.cancel", "run.error",
		"task.start", "task.complete", "task.cancel", "task.error",
		"content.start", "content.delta", "content.end", "content.snapshot", "content.view",
		"planning.start", "planning.delta", "planning.end", "planning.snapshot", "planning.superseded",
		"plan.create", "plan.update", "artifact.publish", "source.publish",
		"awaiting.ask", "awaiting.answer",
	} {
		if !DetailSummary.Allows(eventType, map[string]any{}) {
			t.Fatalf("summary dropped %s", eventType)
		}
	}
	for _, eventType := range []string{
		"reasoning.start", "reasoning.delta", "reasoning.snapshot", "tool.start", "tool.args", "tool.result",
		"tool.output", "tool.wait", "usage.snapshot", "debug.llmChat", "memory.write",
		"context.compact.start", "some.future.event",
	} {
		if DetailSummary.Allows(eventType, map[string]any{}) {
			t.Fatalf("summary leaked %s", eventType)
		}
		if !DetailFull.Allows(eventType, map[string]any{}) {
			t.Fatalf("full dropped %s", eventType)
		}
	}
}

func TestDetailLevelFilterEvents(t *testing.T) {
	events := []EventData{
		{Seq: 1, Type: "request.query", Payload: map[string]any{}},
		{Seq: 2, Type: "reasoning.snapshot", Payload: map[string]any{}},
		{Seq: 3, Type: "content.snapshot", Payload: map[string]any{}},
		{Seq: 4, Type: "tool.snapshot", Payload: map[string]any{}},
	}
	summary := DetailSummary.FilterEvents(events)
	if len(summary) != 2 || summary[0].Seq != 1 || summary[1].Seq != 3 {
		t.Fatalf("summary=%#v", summary)
	}
	if full := DetailFull.FilterEvents(events); len(full) != 4 {
		t.Fatalf("full=%#v", full)
	}
}

func TestDeliveryDisplayDoesNotMutatePersistedEvent(t *testing.T) {
	original := EventData{Type: "run.start", Payload: map[string]any{"runId": "run"}}
	delivered := WithDisplay(original, DetailSummary, DetailFull)
	if original.Payload["display"] != nil {
		t.Fatal("shared event mutated")
	}
	display := delivered.Payload["display"].(map[string]any)
	if display["defaultDetail"] != DetailSummary || display["effectiveDetail"] != DetailFull {
		t.Fatal(display)
	}
}

func TestResolveDetailLevel(t *testing.T) {
	for _, tc := range []struct {
		requested    DetailLevel
		agentDefault string
		want         DetailLevel
	}{
		{"", "", DetailSummary},
		{"", "summary", DetailSummary},
		{"", "verbose", DetailSummary},
		{DetailFull, "summary", DetailFull},
	} {
		if got := ResolveDetailLevel(tc.requested, tc.agentDefault); got != tc.want {
			t.Fatalf("ResolveDetailLevel(%q, %q) = %q, want %q", tc.requested, tc.agentDefault, got, tc.want)
		}
	}
}
