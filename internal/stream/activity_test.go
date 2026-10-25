package stream

import (
	"reflect"
	"testing"
	"time"
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
	tracker := NewActivityTracker("run", "chat", 0)
	var messages []string
	track := func(eventType string, payload map[string]any) {
		keep, derived := tracker.Track(eventType, 0, payload)
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
	// A tool shows once its arguments are complete, not when it starts.
	track("tool.start", map[string]any{"toolId": "t1", "toolName": "web_fetch", "toolLabel": "Fetch Page"})
	track("run.activity", newActivityEvent("run", "chat", "", map[string]any{
		"kind": "tool_run", "toolId": "t1", "toolName": "web_fetch", "toolLabel": "Fetch Page", "text": "example.com",
	}).Payload)
	track("awaiting.ask", map[string]any{"mode": "approval"})
	track("awaiting.answer", map[string]any{"mode": "approval"})
	track("content.start", map[string]any{})
	want := []string{
		"Thinking…", "Thinking…", "Fetch Page: example.com",
		"Waiting for your approval", "Fetch Page: example.com", "",
	}
	if !reflect.DeepEqual(messages, want) {
		t.Fatalf("messages=%#v\nwant %#v", messages, want)
	}
}

func TestActivityTrackerDropsRepeatedActivity(t *testing.T) {
	tracker := NewActivityTracker("run", "chat", 0)
	thinking := newActivityEvent("run", "chat", "", map[string]any{"kind": "thinking"}).Payload
	if keep, _ := tracker.Track("run.activity", 0, thinking); !keep {
		t.Fatal("first activity dropped")
	}
	if keep, _ := tracker.Track("run.activity", 0, thinking); keep {
		t.Fatal("repeated activity kept")
	}
	// A retry always passes and makes the next activity news again.
	retry := newRetryActivityEvent("run", "chat", "", map[string]any{"attempt": 2, "maxAttempts": 3}).Payload
	if keep, _ := tracker.Track("run.activity", 0, retry); !keep {
		t.Fatal("retry dropped")
	}
	if keep, _ := tracker.Track("run.activity", 0, thinking); !keep {
		t.Fatal("activity after retry dropped")
	}
}

func TestActivityTrackerScopesSubtasks(t *testing.T) {
	tracker := NewActivityTracker("run", "chat", 0)
	_, derived := tracker.Track("task.start", 0, map[string]any{"taskId": "child", "taskName": "Research"})
	if len(derived) != 1 || derived[0].Payload["taskId"] != nil || derived[0].Payload["message"] != `Delegating "Research"…` {
		t.Fatalf("delegation must be the parent's activity: %#v", derived)
	}
	_, derived = tracker.Track("content.start", 0, map[string]any{"taskId": "child"})
	if len(derived) != 1 || derived[0].Payload["taskId"] != "child" {
		t.Fatalf("subtask activity lost its scope: %#v", derived)
	}
	tracker.Track("task.complete", 0, map[string]any{"taskId": "child"})
	if _, derived = tracker.Track("content.start", 0, map[string]any{"taskId": "child"}); len(derived) != 1 {
		t.Fatal("a completed subtask must not suppress a later activity of the same id")
	}
	tracker.Track("run.complete", 0, map[string]any{})
	if len(tracker.last) != 0 || len(tracker.beforeAwait) != 0 {
		t.Fatalf("run terminal kept activity state: %#v %#v", tracker.last, tracker.beforeAwait)
	}
}

func TestActivityTrackerIgnoresUnrelatedEvents(t *testing.T) {
	tracker := NewActivityTracker("run", "chat", 0)
	for _, eventType := range []string{"reasoning.delta", "tool.args", "tool.result", "usage.snapshot", "content.delta"} {
		if keep, derived := tracker.Track(eventType, 0, map[string]any{}); !keep || len(derived) != 0 {
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
	publish(0, newActivityEvent("run", "chat", "member", map[string]any{"kind": "compacting"}))
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

func TestVisibleOutputClearsActivity(t *testing.T) {
	tracker := NewActivityTracker("run", "chat", 0)
	tracker.Track("run.activity", 0, newActivityEvent("run", "chat", "", map[string]any{"kind": "thinking"}).Payload)
	for _, eventType := range []string{"content.start", "planning.start"} {
		_, derived := tracker.Track(eventType, 0, map[string]any{})
		if len(derived) != 1 || derived[0].Payload["message"] != "" {
			t.Fatalf("%s must clear the line with an empty message: %#v", eventType, derived)
		}
		// The next real activity is news again.
		if keep, _ := tracker.Track("run.activity", 0, newActivityEvent("run", "chat", "", map[string]any{"kind": "thinking"}).Payload); !keep {
			t.Fatalf("activity after %s dropped", eventType)
		}
	}

	bus := NewRunEventBus(100, 0, nil)
	bus.Publish(newActivityEvent("run", "chat", "", map[string]any{"kind": "thinking"}).Data())
	bus.Publish(newActivityEvent("run", "chat", "", map[string]any{"kind": "idle"}).Data())
	observer, err := bus.Subscribe(0)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-observer.Events:
		t.Fatalf("cleared activity resent on attach: %#v", event)
	default:
	}
}

func TestActivitiesCarryKindNotToolID(t *testing.T) {
	running := newActivityEvent("run", "chat", "", map[string]any{"kind": "tool_run", "toolId": "t1", "toolName": "bash", "text": "list"})
	if running.Payload["toolId"] != nil || running.Payload["kind"] != "tool_run" {
		t.Fatalf("activity must carry its kind and no toolId: %#v", running.Payload)
	}
	announce := newActivityEvent("run", "chat", "", map[string]any{"kind": ActivityKindAnnounce, "text": "Check the config next."})
	if announce.Payload["message"] != "Check the config next." || !IsAnnounceActivity("run.activity", announce.Payload) {
		t.Fatalf("announcement must render its own text: %#v", announce.Payload)
	}
}

func TestAnnouncementHoldsItsScope(t *testing.T) {
	tracker := NewActivityTracker("run", "chat", time.Minute)
	activity := func(kind, text string) map[string]any {
		return newActivityEvent("run", "chat", "", map[string]any{"kind": kind, "text": text, "toolName": "bash"}).Payload
	}
	if keep, _ := tracker.Track("run.activity", 1000, activity(ActivityKindAnnounce, "Read the config loader.")); !keep {
		t.Fatal("announcement dropped")
	}
	if keep, _ := tracker.Track("run.activity", 2000, activity("tool_run", "ls")); keep {
		t.Fatal("tool activity replaced a held announcement")
	}
	if _, derived := tracker.Track("reasoning.start", 3000, map[string]any{}); len(derived) != 0 {
		t.Fatalf("reasoning replaced a held announcement: %#v", derived)
	}
	// Waiting for the user and its answer leave the announcement as it is.
	if _, derived := tracker.Track("awaiting.ask", 4000, map[string]any{"mode": "approval"}); len(derived) != 0 {
		t.Fatalf("awaiting replaced a held announcement: %#v", derived)
	}
	if _, derived := tracker.Track("awaiting.answer", 5000, map[string]any{}); len(derived) != 0 {
		t.Fatalf("answer re-sent the announcement: %#v", derived)
	}
	// A subtask has its own scope.
	if _, derived := tracker.Track("content.start", 6000, map[string]any{"taskId": "child"}); len(derived) != 1 {
		t.Fatal("subtask scope held by the parent announcement")
	}
	// A newer announcement replaces it at once and renews the hold.
	if keep, _ := tracker.Track("run.activity", 30000, activity(ActivityKindAnnounce, "Run the tests.")); !keep {
		t.Fatal("newer announcement dropped")
	}
	if keep, _ := tracker.Track("run.activity", 61000, activity("tool_run", "go test")); keep {
		t.Fatal("hold not renewed by the newer announcement")
	}
	// After the hold the next activity replaces it.
	if keep, _ := tracker.Track("run.activity", 90000, activity("tool_run", "go test")); !keep {
		t.Fatal("activity after the hold dropped")
	}
	if keep, _ := tracker.Track("run.activity", 91000, activity("thinking", "")); !keep {
		t.Fatal("hold came back after it passed")
	}
}

func TestVisibleOutputEndsAnnouncementHold(t *testing.T) {
	tracker := NewActivityTracker("run", "chat", time.Minute)
	tracker.Track("run.activity", 1000, newActivityEvent("run", "chat", "", map[string]any{"kind": ActivityKindAnnounce, "text": "Write the answer."}).Payload)
	if _, derived := tracker.Track("content.start", 2000, map[string]any{}); len(derived) != 1 || derived[0].Payload["message"] != "" {
		t.Fatalf("content must clear a held announcement: %#v", derived)
	}
	if _, derived := tracker.Track("reasoning.start", 3000, map[string]any{}); len(derived) != 1 {
		t.Fatal("hold outlived the visible output")
	}
}

func TestDisabledHoldLetsActivitiesThrough(t *testing.T) {
	tracker := NewActivityTracker("run", "chat", 0)
	tracker.Track("run.activity", 1000, newActivityEvent("run", "chat", "", map[string]any{"kind": ActivityKindAnnounce, "text": "Next."}).Payload)
	if _, derived := tracker.Track("reasoning.start", 1001, map[string]any{}); len(derived) != 1 {
		t.Fatal("zero hold still held")
	}
}

func TestUpstreamRenderedAnnouncementHolds(t *testing.T) {
	tracker := NewActivityTracker("run", "chat", time.Minute)
	rendered := map[string]any{"runId": "run", "kind": ActivityKindAnnounce, "message": "Check upstream."}
	if keep, _ := tracker.Track("run.activity", 1000, rendered); !keep {
		t.Fatal("upstream announcement dropped")
	}
	if _, derived := tracker.Track("reasoning.start", 2000, map[string]any{}); len(derived) != 0 {
		t.Fatalf("local activity replaced an upstream announcement: %#v", derived)
	}
}

func TestEventBusResendsAnnouncementBeforeCurrentActivity(t *testing.T) {
	bus := NewRunEventBus(100, 0, nil)
	bus.Publish(newActivityEvent("run", "chat", "", map[string]any{"kind": ActivityKindAnnounce, "text": "Check the config."}).Data())
	bus.Publish(newActivityEvent("run", "chat", "", map[string]any{"kind": "thinking"}).Data())
	bus.Publish(newActivityEvent("run", "chat", "", map[string]any{"kind": "idle"}).Data())
	observer, err := bus.Subscribe(0)
	if err != nil {
		t.Fatal(err)
	}
	var messages []string
	for len(messages) < 3 {
		select {
		case event := <-observer.Events:
			messages = append(messages, event.String("message"))
			continue
		default:
		}
		break
	}
	// Every level ends on the clear here: output had started.
	if !reflect.DeepEqual(messages, []string{"Check the config.", ""}) {
		t.Fatalf("attach resent %#v", messages)
	}
	if !DetailNone.Allows("run.activity", newActivityEvent("run", "chat", "", map[string]any{"kind": ActivityKindAnnounce, "text": "x"}).Payload) {
		t.Fatal("none must receive a root announcement")
	}
}
