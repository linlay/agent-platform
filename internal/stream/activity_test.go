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

func TestActivityTrackerKeepsRepeatedActivity(t *testing.T) {
	tracker := NewActivityTracker("run", "chat")
	thinking := newActivityEvent("run", "chat", "", map[string]any{"kind": "thinking"}).Payload
	if keep, _ := tracker.Track("run.activity", thinking); !keep {
		t.Fatal("first activity dropped")
	}
	if keep, _ := tracker.Track("run.activity", thinking); !keep {
		t.Fatal("repeated activity dropped")
	}
	// Retries and the activities following them also pass.
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
	tracker := NewActivityTracker("run", "chat")
	tracker.Track("run.activity", newActivityEvent("run", "chat", "", map[string]any{"kind": "thinking"}).Payload)
	for _, eventType := range []string{"content.start", "planning.start"} {
		_, derived := tracker.Track(eventType, map[string]any{})
		if len(derived) != 1 || derived[0].Payload["message"] != "" {
			t.Fatalf("%s must clear the line with an empty message: %#v", eventType, derived)
		}
		// The next real activity is news again.
		if keep, _ := tracker.Track("run.activity", newActivityEvent("run", "chat", "", map[string]any{"kind": "thinking"}).Payload); !keep {
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
		if event.String("kind") != "idle" || event.String("message") != "" {
			t.Fatalf("attach must preserve the clear: %#v", event)
		}
	default:
		t.Fatal("attach did not resend the clear")
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

func TestAnnouncementDoesNotSuppressFullStreamProgress(t *testing.T) {
	tracker := NewActivityTracker("run", "chat")
	announce := newActivityEvent("run", "chat", "", map[string]any{"kind": ActivityKindAnnounce, "text": "Check the config."})
	tracker.Track(announce.Type, announce.Payload)
	for _, kind := range []string{"thinking", "tool_run", "compacting"} {
		event := newActivityEvent("run", "chat", "", map[string]any{"kind": kind})
		if keep, _ := tracker.Track(event.Type, event.Payload); !keep {
			t.Fatalf("full progress %s suppressed", kind)
		}
	}
	for _, eventType := range []string{"reasoning.start", "awaiting.ask", "content.start"} {
		if keep, derived := tracker.Track(eventType, map[string]any{}); !keep || len(derived) != 1 {
			t.Fatalf("%s suppressed: keep=%v derived=%#v", eventType, keep, derived)
		}
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
	if !reflect.DeepEqual(messages, []string{""}) {
		t.Fatalf("attach resent %#v", messages)
	}
	if !DetailSummary.Allows("run.activity", newActivityEvent("run", "chat", "", map[string]any{"kind": ActivityKindAnnounce, "text": "x"}).Payload) {
		t.Fatal("summary must receive a root announcement")
	}
}

func TestAttachPreservesAnnouncementStateAcrossAutomaticActivities(t *testing.T) {
	for _, clear := range []bool{false, true} {
		t.Run(map[bool]string{false: "announcement", true: "cleared"}[clear], func(t *testing.T) {
			bus := NewRunEventBus(100, 0, nil)
			bus.Publish(newActivityEvent("run", "chat", "", map[string]any{"kind": ActivityKindAnnounce, "text": "Check the config."}).Data())
			want := "Check the config."
			if clear {
				bus.Publish(newActivityEvent("run", "chat", "", map[string]any{"kind": "idle"}).Data())
				want = ""
			}
			bus.Publish(newActivityEvent("run", "chat", "", map[string]any{"kind": "thinking"}).Data())
			observer, err := bus.Subscribe(0)
			if err != nil {
				t.Fatal(err)
			}
			var summary, full []string
			for len(observer.Events) > 0 {
				event := <-observer.Events
				full = append(full, event.String("message"))
				if DetailSummary.Allows(event.Type, event.Payload) {
					summary = append(summary, event.String("message"))
				}
			}
			if !reflect.DeepEqual(summary, []string{want}) || !reflect.DeepEqual(full, []string{want, "Thinking…"}) {
				t.Fatalf("summary=%q full=%q, want state %q", summary, full, want)
			}
		})
	}
}

func TestActivityTrackerKeepsRepeatedDerivedActivity(t *testing.T) {
	tracker := NewActivityTracker("run", "chat")
	for _, eventType := range []string{"reasoning.start", "content.start"} {
		for i := 0; i < 2; i++ {
			keep, derived := tracker.Track(eventType, map[string]any{})
			if !keep || len(derived) != 1 {
				t.Fatalf("%s update %d dropped: %#v", eventType, i, derived)
			}
		}
	}
}
