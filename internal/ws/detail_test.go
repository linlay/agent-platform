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
		{RequestFrame{Type: "/api/attach", Detail: "full"}, stream.DetailFull, true},
		{RequestFrame{Type: "/api/chat", Detail: "summary"}, stream.DetailSummary, false},
		{RequestFrame{Type: "/api/query", Detail: "verbose"}, "", true},
		{RequestFrame{Type: "/api/query", Detail: "none"}, "", true},
		{RequestFrame{Type: "/api/chats", Detail: "full"}, stream.DetailFull, true},
	} {
		if got := tc.frame.DetailLevel(); !tc.invalid && got != tc.level {
			t.Fatalf("%#v level=%q want %q", tc.frame, got, tc.level)
		}
		if invalid := tc.frame.detailError() != ""; invalid != tc.invalid {
			t.Fatalf("%#v invalid=%v want %v", tc.frame, invalid, tc.invalid)
		}
	}
}

func TestSummaryStreamDeliversStateNotifications(t *testing.T) {
	conn := NewConn(nil, nil, config.WebSocketConfig{WriteQueueSize: 32, MaxObservesPerConn: 2}, AuthSession{})
	if _, err := conn.ReserveStream("notification-request", "notification-run"); err != nil {
		t.Fatal(err)
	}
	types := []string{"run.access_level.changed", "context.compact.start", "context.compact.complete", "context.compact.failed", "tool.wait", "tool.wait.update", "tool.result", "usage.snapshot", "run.activity", "run.complete"}
	events := make(chan stream.EventData, len(types)+1)
	conn.StartStreamForward("notification-request", &stream.Observer{Events: events}, stream.DetailSummary, stream.DetailSummary)
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
