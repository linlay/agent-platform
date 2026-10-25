package stream

import "testing"

func TestParseDetailLevel(t *testing.T) {
	for raw, want := range map[string]DetailLevel{"": "", "summary": DetailSummary, "full": DetailFull, " full ": DetailFull, "none": DetailNone} {
		if got, ok := ParseDetailLevel(raw); !ok || got != want {
			t.Fatalf("ParseDetailLevel(%q)=%q,%v want %q", raw, got, ok, want)
		}
	}
	for _, raw := range []string{"FULL", "detail", "debug"} {
		if _, ok := ParseDetailLevel(raw); ok {
			t.Fatalf("ParseDetailLevel(%q) accepted", raw)
		}
	}
}

func TestDetailLevelActivities(t *testing.T) {
	progress := newActivityEvent("run", "chat", "", map[string]any{"kind": "thinking"}).Payload
	retry := newRetryActivityEvent("run", "chat", "", map[string]any{"attempt": 2}).Payload
	if !DetailSummary.Allows("run.activity", progress) || DetailSummary.Allows("run.activity", retry) {
		t.Fatal("summary must receive progress and never retries")
	}
	// full is a superset: one full stream serves viewers of either level.
	if !DetailFull.Allows("run.activity", progress) || !DetailFull.Allows("run.activity", retry) {
		t.Fatal("full must receive every activity")
	}
}

func TestNoneDetailIsSummaryWithAnnouncementsOnly(t *testing.T) {
	for eventType := range summaryEventTypes {
		if !DetailNone.Allows(eventType, map[string]any{}) {
			t.Fatalf("none dropped %s", eventType)
		}
	}
	for _, eventType := range []string{"tool.start", "reasoning.start", "usage.snapshot", "some.future.event"} {
		if DetailNone.Allows(eventType, map[string]any{}) {
			t.Fatalf("none leaked %s", eventType)
		}
	}
	for _, payload := range []map[string]any{
		newActivityEvent("run", "chat", "", map[string]any{"kind": ActivityKindAnnounce, "text": "Next."}).Payload,
		newActivityEvent("run", "chat", "member", map[string]any{"kind": ActivityKindAnnounce, "text": "Next."}).Payload,
		newActivityEvent("run", "chat", "", map[string]any{"kind": "idle"}).Payload,
	} {
		if !DetailNone.Allows("run.activity", payload) {
			t.Fatalf("none dropped %#v", payload)
		}
	}
	for _, payload := range []map[string]any{
		newActivityEvent("run", "chat", "", map[string]any{"kind": "thinking"}).Payload,
		newActivityEvent("run", "chat", "", map[string]any{"kind": "tool_run", "toolName": "bash"}).Payload,
		newRetryActivityEvent("run", "chat", "", map[string]any{"attempt": 2}).Payload,
	} {
		if DetailNone.Allows("run.activity", payload) {
			t.Fatalf("none leaked activity %#v", payload)
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
