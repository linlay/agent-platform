package ws

import (
	"testing"
	"time"

	"agent-platform/internal/config"
	"agent-platform/internal/stream"
)

func TestRequestFrameDetail(t *testing.T) {
	for _, tc := range []struct {
		frame   RequestFrame
		level   stream.DetailLevel
		invalid bool
	}{
		{RequestFrame{Type: "/api/query"}, "", false},
		{RequestFrame{Type: "/api/send"}, "", false},
		{RequestFrame{Type: "/api/send", Detail: "simple"}, stream.DetailSimple, false},
		{RequestFrame{Type: "/api/send", Detail: "detailed"}, stream.DetailDetailed, false},
		{RequestFrame{Type: "/api/send", Detail: "verbose"}, "", true},
		{RequestFrame{Type: "/api/attach", Detail: "detailed"}, stream.DetailDetailed, true},
		{RequestFrame{Type: "/api/chat", Detail: "simple"}, stream.DetailSimple, false},
		{RequestFrame{Type: "/api/query", Detail: "verbose"}, "", true},
		{RequestFrame{Type: "/api/query", Detail: "none"}, "", true},
		{RequestFrame{Type: "/api/query", Detail: "summary"}, "", true},
		{RequestFrame{Type: "/api/query", Detail: "full"}, "", true},
		{RequestFrame{Type: "/api/chats", Detail: "detailed"}, stream.DetailDetailed, true},
	} {
		if got := tc.frame.DetailLevel(); !tc.invalid && got != tc.level {
			t.Fatalf("%#v level=%q want %q", tc.frame, got, tc.level)
		}
		if invalid := tc.frame.detailError() != ""; invalid != tc.invalid {
			t.Fatalf("%#v invalid=%v want %v", tc.frame, invalid, tc.invalid)
		}
	}
}

func TestSimpleStreamDeliversStateNotifications(t *testing.T) {
	conn := NewConn(nil, nil, config.WebSocketConfig{WriteQueueSize: 32, MaxObservesPerConn: 2}, AuthSession{})
	if _, err := conn.ReserveStream("notification-request", "notification-run"); err != nil {
		t.Fatal(err)
	}
	types := []string{"run.access_level.changed", "context.compact.start", "context.compact.complete", "context.compact.failed", "tool.wait", "tool.wait.update", "tool.result", "usage.snapshot", "run.activity", "run.complete"}
	events := make(chan stream.EventData, len(types)+1)
	conn.StartStreamForward("notification-request", &stream.Observer{Events: events}, stream.DetailSimple, stream.DetailSimple)
	events <- stream.EventData{Seq: 1, Type: "reasoning.delta", Timestamp: time.Now().UnixMilli(), Payload: map[string]any{"runId": "notification-run", "delta": "hidden"}}
	for i, kind := range types {
		payload := map[string]any{"runId": "notification-run"}
		if kind == "tool.result" {
			payload["toolName"] = "wait"
		}
		if kind == "run.activity" {
			payload["retry"] = map[string]any{"attempt": 2}
		}
		events <- stream.EventData{Seq: int64(i + 2), Type: kind, Timestamp: time.Now().UnixMilli(), Payload: payload}
	}
	close(events)
	for _, kind := range types {
		message := mustReadQueuedMessage(t, conn.writeQueue)
		frame, ok := message.frame.(StreamFrame)
		if !ok || frame.Event == nil || frame.Event.Type != kind {
			t.Fatalf("want %s, got %#v", kind, message.frame)
		}
	}
	message := mustReadQueuedMessage(t, conn.writeQueue)
	frame, ok := message.frame.(StreamFrame)
	if !ok || frame.Reason != "done" {
		t.Fatalf("missing terminal frame: %#v", message.frame)
	}
}

func TestStreamActivityRemovesInternalClassification(t *testing.T) {
	conn := NewConn(nil, nil, config.WebSocketConfig{WriteQueueSize: 8, MaxObservesPerConn: 2}, AuthSession{})
	if _, err := conn.ReserveStream("wait-request", "wait-run"); err != nil {
		t.Fatal(err)
	}
	events := make(chan stream.EventData, 2)
	conn.StartStreamForward("wait-request", &stream.Observer{Events: events}, stream.DetailDetailed, stream.DetailDetailed)
	events <- stream.NewEvent("run.activity", map[string]any{
		"runId": "wait-run", "kind": "model_waiting", "initial": true,
		"activity": map[string]any{"kind": "model_waiting"},
	}).Data()
	events <- stream.NewEvent("run.complete", map[string]any{"runId": "wait-run"}).Data()
	close(events)
	message := mustReadQueuedMessage(t, conn.writeQueue)
	frame, ok := message.frame.(StreamFrame)
	if !ok || frame.Event == nil {
		t.Fatalf("missing activity frame: %#v", message.frame)
	}
	payload := frame.Event.Payload
	if payload["kind"] != nil || payload["initial"] != nil || payload["activity"] != nil || payload["message"] == "" {
		t.Fatalf("classification leaked: %#v", payload)
	}
}
