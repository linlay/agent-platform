package server

import (
	"reflect"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/stream"
)

type proxyPlanningOrderSink struct {
	t       *testing.T
	bus     *stream.RunEventBus
	checked bool

	recordingNotificationSink
}

func (s *proxyPlanningOrderSink) Broadcast(eventType string, payload map[string]any) {
	if eventType == "awaiting.asking" {
		events := replayProxyEventsForTest(s.t, s.bus, 2)
		if events[0].Type != "planning.snapshot" || events[1].Type != "awaiting.ask" {
			s.t.Fatalf("expected planning.snapshot before awaiting.ask when awaiting.asking broadcasts, got %#v", events)
		}
		s.checked = true
	}
	s.recordingNotificationSink.Broadcast(eventType, payload)
}

func TestProxyLiveTextOnlyPlanEmitsPlanningSnapshotBeforeAwaiting(t *testing.T) {
	chats, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	chatID := "chat-proxy-plan-text-only"
	runID := "run-proxy-plan"
	planningID := "run-proxy-plan_planning_1"
	planningText := "# Proxy Plan\n\nBody"
	if _, _, err := chats.EnsureChat(chatID, "proxy-agent", "", "plan it"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	req := api.QueryRequest{
		RequestID: "req-proxy-plan",
		RunID:     runID,
		ChatID:    chatID,
		AgentKey:  "proxy-agent",
		Role:      "user",
		Message:   "plan it",
	}
	eventBus := stream.NewRunEventBus(32, 0, nil)
	notifications := &proxyPlanningOrderSink{t: t, bus: eventBus}
	stepWriter := chat.NewStepWriter(chats, chatID, runID, "CODER")
	recorder := newProxyEventRecorder(
		req,
		catalog.AgentDefinition{Key: "proxy-agent", Mode: "CODER"},
		chats,
		stepWriter,
		nil,
		notifications,
		chat.UsageData{},
		nil,
		config.BillingConfig{},
	)
	if recorder == nil {
		t.Fatal("expected proxy event recorder")
	}

	seq := int64(0)
	published := publishProxyLiveEvent(eventBus, recorder, req, &seq, stream.EventData{
		Type:      "awaiting.ask",
		Timestamp: 1234,
		Payload: map[string]any{
			"awaitingId": "await-proxy-plan",
			"mode":       "plan",
			"timeout":    0,
			"plan": map[string]any{
				"id":         "confirm",
				"planningId": planningID,
				"text":       planningText,
			},
		},
	})
	if published.Type != "awaiting.ask" || published.Seq != 2 {
		t.Fatalf("unexpected published awaiting event %#v", published)
	}

	events := replayProxyEventsForTest(t, eventBus, 2)
	if events[0].Type != "planning.snapshot" || events[0].String("planningId") != planningID ||
		events[0].String("text") != planningText {
		t.Fatalf("unexpected synthesized planning.snapshot %#v", events[0])
	}
	if events[1].Type != "awaiting.ask" {
		t.Fatalf("expected awaiting.ask after planning.snapshot, got %#v", events)
	}
	if got := notifications.EventTypes(); !reflect.DeepEqual(got, []string{"awaiting.asking"}) {
		t.Fatalf("expected awaiting.asking notification, got %#v", got)
	}
	if !notifications.checked {
		t.Fatal("expected awaiting.asking broadcast to observe planning.snapshot before awaiting.ask")
	}
}

func replayProxyEventsForTest(t *testing.T, eventBus *stream.RunEventBus, want int) []stream.EventData {
	t.Helper()
	observer, err := eventBus.Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe event bus: %v", err)
	}
	events := make([]stream.EventData, 0, want)
	for len(events) < want {
		select {
		case event := <-observer.Events:
			events = append(events, event)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %d proxy events, got %#v", want, events)
		}
	}
	return events
}
