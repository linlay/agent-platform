package ws

import (
	"testing"
	"time"

	"agent-platform-runner-go/internal/config"
)

func TestConnRejectsDuplicateRequestID(t *testing.T) {
	conn := NewConn(nil, nil, config.WebSocketConfig{WriteQueueSize: 4}, time.Second, AuthSession{})
	if err := conn.reserveRequest("req_1"); err != nil {
		t.Fatalf("reserve first request: %v", err)
	}
	if err := conn.reserveRequest("req_1"); err == nil {
		t.Fatalf("expected duplicate request error")
	}
}

func TestConnRejectsDuplicateObserve(t *testing.T) {
	conn := NewConn(nil, nil, config.WebSocketConfig{WriteQueueSize: 4, MaxObservesPerConn: 2}, time.Second, AuthSession{})
	if _, err := conn.ReserveStream("req_1", "run_1"); err != nil {
		t.Fatalf("reserve first stream: %v", err)
	}
	if _, err := conn.ReserveStream("req_2", "run_1"); err == nil {
		t.Fatalf("expected duplicate observe error")
	}
}

func TestConnClosesOnWriteQueueOverflow(t *testing.T) {
	conn := NewConn(nil, nil, config.WebSocketConfig{WriteQueueSize: 1}, time.Second, AuthSession{})
	if !conn.SendPush("heartbeat", map[string]any{"timestamp": 1}) {
		t.Fatalf("expected first enqueue to succeed")
	}
	if conn.SendPush("heartbeat", map[string]any{"timestamp": 2}) {
		t.Fatalf("expected second enqueue to fail")
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for !conn.isClosed() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !conn.isClosed() {
		t.Fatalf("expected connection to close after overflow")
	}
}
