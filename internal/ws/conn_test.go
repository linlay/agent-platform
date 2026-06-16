package ws

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/config"
	"agent-platform/internal/i18n"
	"agent-platform/internal/stream"
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

func TestConnLocaleLocalizesErrorsPerConnection(t *testing.T) {
	zhConn := NewConn(nil, nil, config.WebSocketConfig{WriteQueueSize: 4}, time.Second, AuthSession{})
	enConn := NewConn(nil, nil, config.WebSocketConfig{WriteQueueSize: 4}, time.Second, AuthSession{})
	if !zhConn.SetLocale(i18n.LocaleZhCN) {
		t.Fatalf("expected zh-CN locale to be accepted")
	}

	zhConn.SendError("req_1", "not_found", 404, "agent not found", nil)
	enConn.SendError("req_1", "not_found", 404, "agent not found", nil)

	zhMsg := mustReadQueuedMessage(t, zhConn.writeQueue)
	zhFrame, ok := zhMsg.frame.(ErrorFrame)
	if !ok || zhFrame.Msg != "智能体不存在" {
		t.Fatalf("expected localized zh error frame, got %#v", zhMsg.frame)
	}
	enMsg := mustReadQueuedMessage(t, enConn.writeQueue)
	enFrame, ok := enMsg.frame.(ErrorFrame)
	if !ok || enFrame.Msg != "agent not found" {
		t.Fatalf("expected English error frame, got %#v", enMsg.frame)
	}
	data, _ := enFrame.Data.(map[string]any)
	errPayload, _ := data["error"].(map[string]any)
	if errPayload["code"] != "not_found" || errPayload["status"] != 404 {
		t.Fatalf("expected structured error payload in websocket frame, got %#v", enFrame.Data)
	}
}

func TestConnLocaleLocalizesStreamErrorWithoutMutatingOriginal(t *testing.T) {
	conn := NewConn(nil, nil, config.WebSocketConfig{WriteQueueSize: 4, MaxObservesPerConn: 2}, time.Second, AuthSession{})
	conn.SetLocale(i18n.LocaleZhCN)
	if _, err := conn.ReserveStream("req_1", "run_1"); err != nil {
		t.Fatalf("reserve stream: %v", err)
	}
	event := stream.EventData{
		Seq:  1,
		Type: "run.error",
		Payload: map[string]any{
			"error": map[string]any{
				"code":    "not_found",
				"message": "agent not found",
			},
		},
	}
	if !conn.SendStreamEvent("req_1", event) {
		t.Fatalf("expected stream event to be queued")
	}
	msg := mustReadQueuedMessage(t, conn.writeQueue)
	frame, ok := msg.frame.(StreamFrame)
	if !ok || frame.Event == nil {
		t.Fatalf("expected stream frame, got %#v", msg.frame)
	}
	errPayload := frame.Event.Payload["error"].(map[string]any)
	if errPayload["message"] != "智能体不存在" {
		t.Fatalf("expected localized stream error, got %#v", errPayload)
	}
	originalErr := event.Payload["error"].(map[string]any)
	if originalErr["message"] != "agent not found" {
		t.Fatalf("expected original event payload unchanged, got %#v", originalErr)
	}
}

func TestConnDetachRunStreamReleasesObserverAndAllowsReobserve(t *testing.T) {
	conn := NewConn(nil, nil, config.WebSocketConfig{WriteQueueSize: 8, MaxObservesPerConn: 2}, time.Second, AuthSession{})
	streamID, err := conn.ReserveStream("req_1", "run_1")
	if err != nil {
		t.Fatalf("reserve stream: %v", err)
	}

	events := make(chan stream.EventData, 1)
	detachCount := 0
	conn.AttachObserver("req_1", "obs_1", func() {
		detachCount++
		close(events)
	})
	conn.StartStreamForward("req_1", &stream.Observer{Events: events})
	events <- stream.EventData{Seq: 7, Type: "content.delta"}

	msg := mustReadQueuedMessage(t, conn.writeQueue)
	frame, ok := msg.frame.(StreamFrame)
	if !ok || frame.Event == nil || frame.Event.Seq != 7 {
		t.Fatalf("expected forwarded event frame, got %#v", msg.frame)
	}

	detached, ok := conn.DetachRunStream("run_1")
	if !ok {
		t.Fatalf("expected run stream to detach")
	}
	if detached.RunID != "run_1" || detached.StreamRequestID != "req_1" || detached.StreamID != streamID || detached.LastSeq != 7 {
		t.Fatalf("unexpected detach result %#v", detached)
	}
	msg = mustReadQueuedMessage(t, conn.writeQueue)
	frame, ok = msg.frame.(StreamFrame)
	if !ok {
		t.Fatalf("expected detached terminal frame, got %#v", msg.frame)
	}
	if frame.ID != "req_1" || frame.StreamID != streamID || frame.Reason != "detached" || frame.LastSeq != 7 {
		t.Fatalf("unexpected detached terminal frame %#v", frame)
	}
	if detachCount != 1 {
		t.Fatalf("expected detach callback once, got %d", detachCount)
	}
	if _, ok := conn.DetachRunStream("run_1"); ok {
		t.Fatalf("expected second detach to be a no-op")
	}
	if detachCount != 1 {
		t.Fatalf("expected detach callback to remain once, got %d", detachCount)
	}
	if _, err := conn.ReserveStream("req_2", "run_1"); err != nil {
		t.Fatalf("expected run to be observable after detach: %v", err)
	}
	conn.ReleaseStream("req_2")
}

func TestConnClosesOnWriteQueueOverflow(t *testing.T) {
	hub := NewHub()
	conn := NewConn(nil, hub, config.WebSocketConfig{WriteQueueSize: 1}, time.Second, AuthSession{})
	conn.writeQueueFullGrace = 20 * time.Millisecond
	hub.register(conn)
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
	snapshot := hub.MonitorConnections(1, MonitorFilter{SessionID: conn.SessionID()})
	if len(snapshot.Connections) != 1 || snapshot.Connections[0].CloseReason != "write queue full" {
		t.Fatalf("expected monitor close reason, got %#v", snapshot.Connections)
	}
}

func TestConnEnqueueWaitsForTransientWriteQueueBackpressure(t *testing.T) {
	conn := NewConn(nil, nil, config.WebSocketConfig{WriteQueueSize: 1}, time.Second, AuthSession{})
	conn.writeQueueFullGrace = 100 * time.Millisecond
	if !conn.SendPush("first", nil) {
		t.Fatalf("expected first enqueue to succeed")
	}

	drained := make(chan struct{})
	go func() {
		time.Sleep(10 * time.Millisecond)
		<-conn.writeQueue
		close(drained)
	}()

	if !conn.SendPush("second", nil) {
		t.Fatalf("expected second enqueue to wait for queue drain")
	}
	<-drained
	if conn.isClosed() {
		t.Fatalf("did not expect transient backpressure to close connection")
	}
	msg := mustReadQueuedMessage(t, conn.writeQueue)
	push, ok := msg.frame.(PushFrame)
	if !ok || push.Type != "second" {
		t.Fatalf("expected queued second push, got %#v", msg.frame)
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

func TestConnStartStreamForwardMapsExpiredRunErrorToErrorReason(t *testing.T) {
	conn := NewConn(nil, nil, config.WebSocketConfig{WriteQueueSize: 8, MaxObservesPerConn: 2}, time.Second, AuthSession{})
	if _, err := conn.ReserveStream("req_1", "run_1"); err != nil {
		t.Fatalf("reserve stream: %v", err)
	}

	events := make(chan stream.EventData, 2)
	conn.StartStreamForward("req_1", &stream.Observer{Events: events})

	events <- stream.EventData{
		Seq:       1,
		Type:      "run.error",
		Timestamp: time.Now().UnixMilli(),
		Payload: map[string]any{
			"runId": "run_1",
			"error": map[string]any{
				"code":     "expired",
				"message":  "run expired",
				"scope":    "run",
				"category": "runtime",
			},
		},
	}
	close(events)

	msg := mustReadQueuedMessage(t, conn.writeQueue)
	frame, ok := msg.frame.(StreamFrame)
	if !ok || frame.Event == nil || frame.Event.Type != "run.error" {
		t.Fatalf("expected first queued stream event to be run.error, got %#v", msg.frame)
	}

	msg = mustReadQueuedMessage(t, conn.writeQueue)
	frame, ok = msg.frame.(StreamFrame)
	if !ok {
		t.Fatalf("expected terminal stream frame, got %#v", msg.frame)
	}
	if frame.Reason != "error" || frame.LastSeq != 1 {
		t.Fatalf("expected error terminal frame, got %#v", frame)
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
