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
	"agent-platform/internal/timecontract"
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
	startServerFixtureRun(t, chats, chatID, runID, testEpochMillis)

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
		1_700_000_000_000,
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
	published, err := publishProxyLiveEvent(eventBus, recorder, req, &seq, stream.EventData{
		Type:      "awaiting.ask",
		Timestamp: 1_700_000_000_000,
		Payload: map[string]any{
			"awaitingId": "await-proxy-plan",
			"mode":       "planning",
			"timeout":    0,
			"planning": map[string]any{
				"id":         "confirm",
				"planningId": planningID,
				"text":       planningText,
			},
		},
	})
	if err != nil {
		t.Fatalf("publish proxy live event: %v", err)
	}
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

func TestDecodeProxyFrameRejectsInvalidUpstreamTimestamp(t *testing.T) {
	for name, frame := range map[string]string{
		"string":  `{"frame":"stream","id":"request-1","event":{"type":"content.delta","timestamp":"1700000000000"}}`,
		"float":   `{"frame":"stream","id":"request-1","event":{"type":"content.delta","timestamp":1700000000000.5}}`,
		"seconds": `{"frame":"stream","id":"request-1","event":{"type":"content.delta","timestamp":1700000000}}`,
		"zero":    `{"frame":"stream","id":"request-1","event":{"type":"content.delta","timestamp":0}}`,
		"missing": `{"frame":"stream","id":"request-1","event":{"type":"content.delta"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := decodeProxyFrameAt([]byte(frame), "proxy.websocket.event")
			if !timecontract.IsViolation(err) {
				t.Fatalf("expected time contract violation, got %v", err)
			}
			data := timecontract.ErrorData(err)
			if data["field"] != "timestamp" || data["location"] != "proxy.websocket.event" || data["expected"] != timecontract.Expected {
				t.Fatalf("unexpected violation data %#v", data)
			}
		})
	}
}

func TestPublishProxyLiveEventDoesNotFillMissingTimestamp(t *testing.T) {
	bus := stream.NewRunEventBus(4, 0, nil)
	_, err := publishProxyLiveEvent(bus, nil, api.QueryRequest{RunID: "run-1", ChatID: "chat-1"}, new(int64), stream.EventData{
		Type:    "content.delta",
		Payload: map[string]any{"delta": "hello"},
	})
	if !timecontract.IsViolation(err) {
		t.Fatalf("expected time contract violation, got %v", err)
	}
}

func TestProxyRunErrorEventUsesLocalTimestampForContractViolation(t *testing.T) {
	err := timecontract.ValidateEpochMillis(0, "timestamp", "proxy.sse.event")
	event := proxyRunErrorEvent(api.QueryRequest{RunID: "run-1", ChatID: "chat-1"}, err)
	if event.Type != "run.error" {
		t.Fatalf("event type = %q", event.Type)
	}
	if validateErr := timecontract.ValidateEpochMillis(event.Timestamp, "timestamp", "test"); validateErr != nil {
		t.Fatalf("local error timestamp must be valid: %v", validateErr)
	}
	if event.Payload["code"] != "time_contract_violation" || event.Payload["field"] != "timestamp" || event.Payload["location"] != "proxy.sse.event" {
		t.Fatalf("unexpected local error payload %#v", event.Payload)
	}
}

func TestProxyEventRecorderFinishKeepsCompletionTimestampWhenPersistenceFails(t *testing.T) {
	store, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-proxy-finish-error", "proxy-agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	startServerFixtureRun(t, store, "chat-proxy-finish-error", "run-proxy-finish-error", testEpochMillis)
	recorder := newProxyEventRecorder(
		api.QueryRequest{
			ChatID:   "chat-proxy-finish-error",
			RunID:    "run-proxy-finish-error",
			AgentKey: "proxy-agent",
			Message:  "hello",
		},
		1_700_000_000_000,
		catalog.AgentDefinition{Key: "proxy-agent", Mode: "PROXY"},
		store,
		chat.NewStepWriter(store, "chat-proxy-finish-error", "run-proxy-finish-error", "PROXY"),
		nil,
		nil,
		chat.UsageData{},
		nil,
		config.BillingConfig{},
	)
	if recorder == nil {
		t.Fatal("expected proxy event recorder")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close chat store: %v", err)
	}

	persisted, completion := recorder.Finish()
	if persisted {
		t.Fatal("expected closed store completion to fail persistence")
	}
	if completion.StartedAtMillis != 1_700_000_000_000 {
		t.Fatalf("unexpected retained start timestamp %#v", completion)
	}
	if err := timecontract.ValidateEpochMillis(completion.UpdatedAtMillis, "completedAt", "test"); err != nil {
		t.Fatalf("completion timestamp must be retained despite persistence failure: %v (%#v)", err, completion)
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
