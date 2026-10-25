package stream

import (
	"reflect"
	"testing"
)

func activityMessages(events []StreamEvent) []string {
	var out []string
	for _, event := range events {
		if event.Type == "run.activity" {
			out = append(out, anyString(event.Payload["message"]))
		}
	}
	return out
}

func TestActivityTrackerDerivesProgressInOrder(t *testing.T) {
	tracker := NewActivityTracker("run", "chat")
	var messages []string
	track := func(eventType string, payload map[string]any) {
		keep, derived := tracker.Track(eventType, payload)
		if !keep {
			return
		}
		if eventType == "run.activity" {
			messages = append(messages, anyString(payload["message"]))
		}
		messages = append(messages, activityMessages(derived)...)
	}
	track("run.activity", newActivityEvent("run", "chat", "", map[string]any{"kind": "thinking"}).Payload)
	track("reasoning.start", map[string]any{"reasoningId": ""})
	track("tool.start", map[string]any{"toolId": "t1", "toolName": "web_fetch", "toolLabel": "Fetch Page"})
	// The second preparation of the same tool says the same thing.
	track("tool.start", map[string]any{"toolId": "t2", "toolName": "web_fetch", "toolLabel": "Fetch Page"})
	track("run.activity", newActivityEvent("run", "chat", "", map[string]any{
		"kind": "tool_run", "toolId": "t1", "toolName": "web_fetch", "toolLabel": "Fetch Page", "text": "example.com",
	}).Payload)
	track("awaiting.ask", map[string]any{"mode": "approval"})
	track("awaiting.answer", map[string]any{"mode": "approval"})
	track("content.start", map[string]any{})
	want := []string{
		"Thinking…", "Thinking…", `Preparing "Fetch Page"…`, "Fetch Page: example.com",
		"Waiting for your approval", "Fetch Page: example.com", "Responding…",
	}
	if !reflect.DeepEqual(messages, want) {
		t.Fatalf("messages=%#v\nwant %#v", messages, want)
	}
}

func TestActivityTrackerDropsRepeatedActivity(t *testing.T) {
	tracker := NewActivityTracker("run", "chat")
	thinking := newActivityEvent("run", "chat", "", map[string]any{"kind": "thinking"}).Payload
	if keep, _ := tracker.Track("run.activity", thinking); !keep {
		t.Fatal("first activity dropped")
	}
	if keep, _ := tracker.Track("run.activity", thinking); keep {
		t.Fatal("repeated activity kept")
	}
	// A retry always passes and makes the next activity news again.
	retry := newRetryActivityEvent("run", "chat", "", map[string]any{"attempt": 2, "maxAttempts": 3}).Payload
	if keep, _ := tracker.Track("run.activity", retry); !keep {
		t.Fatal("retry dropped")
	}
	if keep, _ := tracker.Track("run.activity", thinking); !keep {
		t.Fatal("activity after retry dropped")
	}
}

func TestActivityTrackerScopesSubtasks(t *testing.T) {
	tracker := NewActivityTracker("run", "chat")
	_, derived := tracker.Track("task.start", map[string]any{"taskId": "child", "taskName": "Research"})
	if len(derived) != 1 || derived[0].Payload["taskId"] != nil || derived[0].Payload["message"] != `Delegating "Research"…` {
		t.Fatalf("delegation must be the parent's activity: %#v", derived)
	}
	_, derived = tracker.Track("content.start", map[string]any{"taskId": "child"})
	if len(derived) != 1 || derived[0].Payload["taskId"] != "child" {
		t.Fatalf("subtask activity lost its scope: %#v", derived)
	}
	tracker.Track("task.complete", map[string]any{"taskId": "child"})
	if _, derived = tracker.Track("content.start", map[string]any{"taskId": "child"}); len(derived) != 1 {
		t.Fatal("a completed subtask must not suppress a later activity of the same id")
	}
	tracker.Track("run.complete", map[string]any{})
	if len(tracker.last) != 0 || len(tracker.beforeAwait) != 0 {
		t.Fatalf("run terminal kept activity state: %#v %#v", tracker.last, tracker.beforeAwait)
	}
}

func TestActivityTrackerIgnoresUnrelatedEvents(t *testing.T) {
	tracker := NewActivityTracker("run", "chat")
	for _, eventType := range []string{"reasoning.delta", "tool.args", "tool.result", "usage.snapshot", "content.delta"} {
		if keep, derived := tracker.Track(eventType, map[string]any{}); !keep || len(derived) != 0 {
			t.Fatalf("%s derived %#v keep=%v", eventType, derived, keep)
		}
	}
}

func TestEventBusResendsCurrentActivityOnAttach(t *testing.T) {
	bus := NewRunEventBus(100, 0, nil)
	publish := func(seq int64, event StreamEvent) {
		data := event.Data()
		data.Seq = seq
		bus.Publish(data)
	}
	publish(0, newActivityEvent("run", "chat", "", map[string]any{"kind": "thinking"}))
	publish(0, newActivityEvent("run", "chat", "member", map[string]any{"kind": "responding"}))
	publish(1, newRetryActivityEvent("run", "chat", "", map[string]any{"attempt": 2}))
	publish(2, NewEvent("task.complete", map[string]any{"taskId": "member"}))
	if bus.LatestSeq() != 2 || bus.OldestSeq() != 1 {
		t.Fatalf("progress activity entered the replay log: latest=%d oldest=%d", bus.LatestSeq(), bus.OldestSeq())
	}

	observer, err := bus.Subscribe(2)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-observer.Events:
		if event.Type != "run.activity" || event.Seq != 0 || event.Value("message") != "Thinking…" {
			t.Fatalf("unexpected attach snapshot %#v", event)
		}
	default:
		t.Fatal("current activity not resent")
	}
	select {
	case event := <-observer.Events:
		t.Fatalf("finished subtask or retry resent: %#v", event)
	default:
	}
	publish(3, NewEvent("run.complete", map[string]any{}))
	late, err := bus.Subscribe(3)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-late.Events:
		t.Fatalf("activity resent after run terminal: %#v", event)
	default:
	}
}
