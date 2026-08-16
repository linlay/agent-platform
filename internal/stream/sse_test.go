package stream

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEncodeJSONFrameMatchesLiveSSEWireFormat(t *testing.T) {
	raw, length, err := EncodeJSONFrame("message", map[string]any{
		"seq":       1,
		"type":      "content.snapshot",
		"text":      "hello\nworld",
		"timestamp": int64(1_700_000_000_000),
	})
	if err != nil {
		t.Fatalf("encode json frame: %v", err)
	}
	if raw != "event: message\ndata: {\"seq\":1,\"text\":\"hello\\nworld\",\"timestamp\":1700000000000,\"type\":\"content.snapshot\"}\n\n" {
		t.Fatalf("unexpected frame: %q", raw)
	}
	if length != len(`{"seq":1,"text":"hello\nworld","timestamp":1700000000000,"type":"content.snapshot"}`) {
		t.Fatalf("payload length=%d", length)
	}
}

func TestWriterWritesImmediatelyWhenBufferingDisabled(t *testing.T) {
	rec := httptest.NewRecorder()
	writer, err := NewWriter(rec, Options{})
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	defer writer.Close()

	if err := writer.WriteJSON("message", map[string]any{
		"type":   "content.delta",
		"runId":  "run_1",
		"chatId": "chat_1",
		"delta":  "hello",
	}); err != nil {
		t.Fatalf("write json: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, `"type":"content.delta"`) {
		t.Fatalf("expected immediate sse output, got %s", body)
	}
}

func TestWriterFlushesBufferedEventsOnTerminalFrame(t *testing.T) {
	rec := httptest.NewRecorder()
	writer, err := NewWriter(rec, Options{
		Render: RenderConfig{
			MaxBufferedEvents:    8,
			HeartbeatPassThrough: true,
		},
	})
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	defer writer.Close()

	if err := writer.WriteJSON("message", map[string]any{
		"type":   "content.delta",
		"runId":  "run_1",
		"chatId": "chat_1",
		"delta":  "hello",
	}); err != nil {
		t.Fatalf("write json: %v", err)
	}
	if strings.Contains(rec.Body.String(), `"type":"content.delta"`) {
		t.Fatalf("expected content to stay buffered before terminal event")
	}
	if err := writer.WriteJSON("message", map[string]any{
		"type":   "run.complete",
		"runId":  "run_1",
		"chatId": "chat_1",
	}); err != nil {
		t.Fatalf("write terminal json: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, `"type":"content.delta"`) || !strings.Contains(body, `"type":"run.complete"`) {
		t.Fatalf("expected buffered event and terminal event after flush, got %s", body)
	}
}

func TestWriterFlushesHeartbeatWhenPassThroughEnabled(t *testing.T) {
	rec := httptest.NewRecorder()
	writer, err := NewWriter(rec, Options{
		Render: RenderConfig{
			MaxBufferedEvents:    8,
			HeartbeatPassThrough: true,
		},
	})
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	defer writer.Close()

	if err := writer.WriteJSON("message", map[string]any{
		"type":   "content.delta",
		"runId":  "run_1",
		"chatId": "chat_1",
		"delta":  "hello",
	}); err != nil {
		t.Fatalf("write json: %v", err)
	}
	if err := writer.WriteComment("heartbeat"); err != nil {
		t.Fatalf("write heartbeat: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, `"type":"content.delta"`) {
		t.Fatalf("expected pending event flushed before heartbeat, got %s", body)
	}
	if !strings.Contains(body, ": heartbeat") {
		t.Fatalf("expected heartbeat comment, got %s", body)
	}
}

func TestWriterFlushesByInterval(t *testing.T) {
	rec := httptest.NewRecorder()
	writer, err := NewWriter(rec, Options{
		Render: RenderConfig{
			FlushInterval:        1,
			HeartbeatPassThrough: true,
		},
	})
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	defer writer.Close()

	if err := writer.WriteJSON("message", map[string]any{
		"type":   "content.delta",
		"runId":  "run_1",
		"chatId": "chat_1",
		"delta":  "hello",
	}); err != nil {
		t.Fatalf("write json: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)

	if !strings.Contains(rec.Body.String(), `"type":"content.delta"`) {
		t.Fatalf("expected buffered event flushed by timer, got %s", rec.Body.String())
	}
}
