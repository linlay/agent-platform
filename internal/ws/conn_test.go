package ws

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/stream"
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

func TestConnSourceKeepsAsyncDispatchAndConnectedOrdering(t *testing.T) {
	t.Helper()
	root, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "conn.go"))
	if err != nil {
		t.Fatalf("read conn.go: %v", err)
	}
	source := string(data)
	if !strings.Contains(source, `go dispatch(c.Context(), c, req)`) {
		t.Fatalf("expected Run() to dispatch requests asynchronously")
	}
	writeLoopIdx := strings.Index(source, `go c.writeLoop()`)
	connectedIdx := strings.Index(source, `c.SendPush("connected", map[string]any{"sessionId": c.sessionID})`)
	if writeLoopIdx < 0 || connectedIdx < 0 {
		t.Fatalf("expected Run() to start writer and send connected push")
	}
	if writeLoopIdx > connectedIdx {
		t.Fatalf("expected connected push to be enqueued after writeLoop starts")
	}
}

func TestConnStartStreamForwardMapsRunExpiredToExpiredReason(t *testing.T) {
	conn := NewConn(nil, nil, config.WebSocketConfig{WriteQueueSize: 8, MaxObservesPerConn: 2}, time.Second, AuthSession{})
	if _, err := conn.ReserveStream("req_1", "run_1"); err != nil {
		t.Fatalf("reserve stream: %v", err)
	}

	events := make(chan stream.EventData, 2)
	conn.StartStreamForward("req_1", &stream.Observer{Events: events})

	events <- stream.EventData{
		Seq:       1,
		Type:      "run.expired",
		Timestamp: time.Now().UnixMilli(),
		Payload:   map[string]any{"runId": "run_1"},
	}
	close(events)

	msg := mustReadQueuedMessage(t, conn.writeQueue)
	frame, ok := msg.frame.(StreamFrame)
	if !ok || frame.Event == nil || frame.Event.Type != "run.expired" {
		t.Fatalf("expected first queued stream event to be run.expired, got %#v", msg.frame)
	}

	msg = mustReadQueuedMessage(t, conn.writeQueue)
	frame, ok = msg.frame.(StreamFrame)
	if !ok {
		t.Fatalf("expected terminal stream frame, got %#v", msg.frame)
	}
	if frame.Reason != "expired" || frame.LastSeq != 1 {
		t.Fatalf("expected expired terminal frame, got %#v", frame)
	}
}

func TestConnStartStreamForwardMarksObserverDoneAfterTerminalFrame(t *testing.T) {
	conn := NewConn(nil, nil, config.WebSocketConfig{WriteQueueSize: 8, MaxObservesPerConn: 2}, time.Second, AuthSession{})
	if _, err := conn.ReserveStream("req_1", "run_1"); err != nil {
		t.Fatalf("reserve stream: %v", err)
	}

	bus := stream.NewRunEventBus(16, 0, nil)
	observer, err := bus.Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe observer: %v", err)
	}

	conn.StartStreamForward("req_1", observer)
	bus.Publish(stream.EventData{
		Seq:       1,
		Type:      "run.complete",
		Timestamp: time.Now().UnixMilli(),
		Payload:   map[string]any{"runId": "run_1"},
	})

	done := make(chan struct{})
	go func() {
		bus.FreezeAndWait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for stream forward to drain")
	}

	msg := mustReadQueuedMessage(t, conn.writeQueue)
	frame, ok := msg.frame.(StreamFrame)
	if !ok || frame.Event == nil || frame.Event.Type != "run.complete" {
		t.Fatalf("expected queued run.complete event, got %#v", msg.frame)
	}

	msg = mustReadQueuedMessage(t, conn.writeQueue)
	frame, ok = msg.frame.(StreamFrame)
	if !ok {
		t.Fatalf("expected terminal stream frame, got %#v", msg.frame)
	}
	if frame.Reason != "done" || frame.LastSeq != 1 {
		t.Fatalf("expected done terminal frame, got %#v", frame)
	}
}

func TestConnConnectedPushPayload(t *testing.T) {
	conn := NewConn(nil, nil, config.WebSocketConfig{WriteQueueSize: 4}, time.Second, AuthSession{})
	if !conn.SendPush("connected", map[string]any{"sessionId": conn.SessionID()}) {
		t.Fatalf("expected connected push to enqueue")
	}

	msg := mustReadQueuedMessage(t, conn.writeQueue)
	frame, ok := msg.frame.(PushFrame)
	if !ok {
		t.Fatalf("expected push frame, got %T", msg.frame)
	}
	if frame.Type != "connected" {
		t.Fatalf("expected connected push, got %#v", frame)
	}
	data, ok := frame.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected connected data payload, got %#v", frame.Data)
	}
	sessionID, _ := data["sessionId"].(string)
	if sessionID == "" {
		t.Fatalf("expected non-empty sessionId, got %#v", frame.Data)
	}
}

func mustReadQueuedMessage(t *testing.T, queue <-chan outboundMessage) outboundMessage {
	t.Helper()
	select {
	case msg := <-queue:
		return msg
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for queued websocket message")
		return outboundMessage{}
	}
}
