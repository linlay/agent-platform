package stream

import "testing"

func TestParseDetailLevel(t *testing.T) {
	for raw, want := range map[string]DetailLevel{"": "", "simple": DetailSimple, "detailed": DetailDetailed, " detailed ": DetailDetailed} {
		if got, ok := ParseDetailLevel(raw); !ok || got != want {
			t.Fatalf("ParseDetailLevel(%q)=%q,%v want %q", raw, got, ok, want)
		}
	}
	for _, raw := range []string{"DETAILED", "summary", "full", "detail", "debug", "none"} {
		if _, ok := ParseDetailLevel(raw); ok {
			t.Fatalf("ParseDetailLevel(%q) accepted", raw)
		}
	}
}

func TestDetailLevelActivities(t *testing.T) {
	progress := newActivityEvent("run", "chat", "", map[string]any{"kind": "thinking"}).Payload
	retry := newRetryActivityEvent("run", "chat", "", map[string]any{"attempt": 2}).Payload
	if DetailSimple.Allows("run.activity", progress) || !DetailSimple.Allows("run.activity", retry) {
		t.Fatal("simple must hide automatic progress and deliver retries")
	}
	// detailed is a superset: one detailed stream serves viewers of either level.
	if !DetailDetailed.Allows("run.activity", progress) || !DetailDetailed.Allows("run.activity", retry) {
		t.Fatal("detailed must receive every activity")
	}
}

func TestSimpleDetailDeliversOnlyAnnouncementsClearsAndRetries(t *testing.T) {
	for eventType := range simpleEventTypes {
		if eventType == "run.activity" {
			continue
		}
		if !DetailSimple.Allows(eventType, map[string]any{}) {
			t.Fatalf("simple dropped %s", eventType)
		}
	}
	for _, eventType := range []string{"tool.start", "reasoning.start", "some.future.event"} {
		if DetailSimple.Allows(eventType, map[string]any{}) {
			t.Fatalf("simple leaked %s", eventType)
		}
	}
	for _, payload := range []map[string]any{
		newActivityEvent("run", "chat", "", map[string]any{"kind": ActivityKindAnnounce, "text": "Next."}).Payload,
		newActivityEvent("run", "chat", "member", map[string]any{"kind": ActivityKindAnnounce, "text": "Next."}).Payload,
		newActivityEvent("run", "chat", "", map[string]any{"kind": "idle"}).Payload,
		newRetryActivityEvent("run", "chat", "", map[string]any{"attempt": 2}).Payload,
	} {
		if !DetailSimple.Allows("run.activity", payload) {
			t.Fatalf("simple dropped %#v", payload)
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
	} {
		if DetailSimple.Allows("run.activity", payload) {
			t.Fatalf("simple leaked activity %#v", payload)
		}
	}
	// Proxy activities may already be rendered and have only a top-level kind.
	for _, kind := range []string{"thinking", "reasoning", "tool_run", "subtask", "awaiting", "compacting", "future_activity", ""} {
		if DetailSimple.Allows("run.activity", map[string]any{"kind": kind}) {
			t.Fatalf("simple leaked rendered %s", kind)
		}
	}
	for _, kind := range []string{ActivityKindAnnounce, "idle"} {
		if !DetailSimple.Allows("run.activity", map[string]any{"kind": kind}) {
			t.Fatalf("simple dropped rendered %s", kind)
		}
	}
}

func TestSimpleDetailKeepsNotificationsWithoutToolDetails(t *testing.T) {
	for _, eventType := range []string{
		"request.query", "request.steer", "request.submit", "chat.start",
		"run.start", "run.complete", "run.cancel", "run.error",
		"task.start", "task.complete", "task.cancel", "task.error",
		"content.start", "content.delta", "content.end", "content.snapshot", "content.view",
		"planning.start", "planning.delta", "planning.end", "planning.snapshot", "planning.superseded",
		"plan.create", "plan.update", "artifact.publish", "source.publish",
		"awaiting.ask", "awaiting.answer",
		"run.access_level.changed", "context.compact.start", "context.compact.complete", "context.compact.failed",
		"tool.wait", "tool.wait.update", "usage.snapshot",
	} {
		if !DetailSimple.Allows(eventType, map[string]any{}) {
			t.Fatalf("simple dropped %s", eventType)
		}
	}
	for _, eventType := range []string{
		"reasoning.start", "reasoning.delta", "reasoning.snapshot", "tool.start", "tool.args", "tool.result",
		"tool.output", "debug.llmChat", "memory.write", "some.future.event",
	} {
		if DetailSimple.Allows(eventType, map[string]any{}) {
			t.Fatalf("simple leaked %s", eventType)
		}
		if !DetailDetailed.Allows(eventType, map[string]any{}) {
			t.Fatalf("detailed dropped %s", eventType)
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
	simple := DetailSimple.FilterEvents(events)
	if len(simple) != 2 || simple[0].Seq != 1 || simple[1].Seq != 3 {
		t.Fatalf("simple=%#v", simple)
	}
	if detailed := DetailDetailed.FilterEvents(events); len(detailed) != 4 {
		t.Fatalf("detailed=%#v", detailed)
	}
}

func TestDeliveryDisplayDoesNotMutatePersistedEvent(t *testing.T) {
	original := EventData{Type: "run.start", Payload: map[string]any{"runId": "run"}}
	delivered := WithDisplay(original, DetailSimple, DetailDetailed)
	if original.Payload["display"] != nil {
		t.Fatal("shared event mutated")
	}
	display := delivered.Payload["display"].(map[string]any)
	if display["defaultDetail"] != DetailSimple || display["effectiveDetail"] != DetailDetailed {
		t.Fatal(display)
	}
}

func TestResolveDetailLevel(t *testing.T) {
	for _, tc := range []struct {
		requested    DetailLevel
		agentDefault string
		want         DetailLevel
	}{
		{"", "", DetailSimple},
		{"", "simple", DetailSimple},
		{"", "verbose", DetailSimple},
		{DetailDetailed, "simple", DetailDetailed},
	} {
		if got := ResolveDetailLevel(tc.requested, tc.agentDefault); got != tc.want {
			t.Fatalf("ResolveDetailLevel(%q, %q) = %q, want %q", tc.requested, tc.agentDefault, got, tc.want)
		}
	}
}

// A wait notification without its closing result would leave the interactive card stale.
func TestSimpleDeliversWaitCompletionWithoutOrdinaryToolResults(t *testing.T) {
	events := []EventData{
		{Seq: 1, Type: "tool.wait", Payload: map[string]any{"toolName": "wait"}},
		{Seq: 2, Type: "tool.wait.update", Payload: map[string]any{"toolName": "wait"}},
		{Seq: 3, Type: "tool.result", Payload: map[string]any{"toolName": "wait"}},
		{Seq: 4, Type: "tool.result", Payload: map[string]any{"toolName": "bash"}},
	}
	delivered := DetailSimple.FilterEvents(events)
	if len(delivered) != 3 || delivered[2].Seq != 3 {
		t.Fatalf("wait lifecycle: %#v", delivered)
	}
}
