package stream

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agent-platform-runner-go/internal/config"
)

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
		Render: config.H2ARenderConfig{
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
		Render: config.H2ARenderConfig{
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
		Render: config.H2ARenderConfig{
			FlushIntervalMs:      10,
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
	time.Sleep(30 * time.Millisecond)

	if !strings.Contains(rec.Body.String(), `"type":"content.delta"`) {
		t.Fatalf("expected buffered event flushed by timer, got %s", rec.Body.String())
	}
}
