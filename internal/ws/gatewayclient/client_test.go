package gatewayclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/ws"

	gws "github.com/gorilla/websocket"
)

type acceptedGatewayConn struct {
	conn          *gws.Conn
	authorization string
}

type testAuthenticator struct{}

func (testAuthenticator) VerifyToken(ctx context.Context, token string) (ws.AuthSession, error) {
	return ws.AuthSession{Context: ctx}, nil
}

func TestClientConnectDispatchBroadcastAndReconnect(t *testing.T) {
	accepted := make(chan acceptedGatewayConn, 4)
	upgrader := gws.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := strings.TrimSpace(r.Header.Get("Authorization")); got != "Bearer dev-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade gateway connection: %v", err)
			return
		}
		accepted <- acceptedGatewayConn{conn: conn, authorization: r.Header.Get("Authorization")}
	}))
	defer server.Close()

	hub := ws.NewHub()
	wsCfg := config.WebSocketConfig{
		Enabled:             true,
		MaxMessageSizeBytes: 1 << 20,
		PingIntervalMs:      200,
		WriteTimeoutMs:      1000,
		WriteQueueSize:      8,
		MaxObservesPerConn:  4,
	}
	handler := ws.NewHandler(wsCfg, 50*time.Millisecond, hub, testAuthenticator{})
	handler.RegisterRoute("/api/agents", func(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
		conn.SendResponse(req.Type, req.ID, 0, "success", map[string]any{"agents": []string{"demo"}})
		conn.CompleteRequest(req.ID)
	})

	client := New(Config{
		URL:              wsURL(server.URL),
		Token:            "dev-token",
		HandshakeTimeout: 500 * time.Millisecond,
		ReconnectMin:     30 * time.Millisecond,
		ReconnectMax:     80 * time.Millisecond,
	}, wsCfg, 50*time.Millisecond, hub, handler.Dispatch)
	client.Start(context.Background())
	defer func() {
		_ = client.Stop()
	}()

	first := waitAcceptedConn(t, accepted)
	defer first.conn.Close()
	if first.authorization != "Bearer dev-token" {
		t.Fatalf("expected Authorization header to be forwarded, got %q", first.authorization)
	}
	connected := waitForPush(t, first.conn, "connected")
	if connected.Frame != ws.FramePush {
		t.Fatalf("expected push frame, got %#v", connected)
	}

	if err := first.conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    "/api/agents",
		ID:      "req_agents",
		Payload: ws.MarshalPayload(map[string]any{}),
	}); err != nil {
		t.Fatalf("write agents request: %v", err)
	}
	response := waitForResponse(t, first.conn, "req_agents")
	if response.Frame != ws.FrameResponse || response.Type != "/api/agents" || response.Code != 0 {
		t.Fatalf("unexpected agents response: %#v", response)
	}

	hub.Broadcast("catalog.updated", map[string]any{"reason": "test"})
	broadcast := waitForPush(t, first.conn, "catalog.updated")
	if broadcast.Type != "catalog.updated" {
		t.Fatalf("expected catalog.updated push, got %#v", broadcast)
	}

	if err := first.conn.Close(); err != nil {
		t.Fatalf("close first gateway connection: %v", err)
	}

	second := waitAcceptedConn(t, accepted)
	defer second.conn.Close()
	waitForPush(t, second.conn, "connected")
}

func TestClientStopClosesActiveConnection(t *testing.T) {
	accepted := make(chan acceptedGatewayConn, 2)
	upgrader := gws.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade gateway connection: %v", err)
			return
		}
		accepted <- acceptedGatewayConn{conn: conn, authorization: r.Header.Get("Authorization")}
	}))
	defer server.Close()

	hub := ws.NewHub()
	wsCfg := config.WebSocketConfig{
		Enabled:             true,
		MaxMessageSizeBytes: 1 << 20,
		PingIntervalMs:      200,
		WriteTimeoutMs:      1000,
		WriteQueueSize:      8,
		MaxObservesPerConn:  4,
	}
	handler := ws.NewHandler(wsCfg, 50*time.Millisecond, hub, testAuthenticator{})
	client := New(Config{
		URL:              wsURL(server.URL),
		Token:            "dev-token",
		HandshakeTimeout: 500 * time.Millisecond,
		ReconnectMin:     30 * time.Millisecond,
		ReconnectMax:     80 * time.Millisecond,
	}, wsCfg, 50*time.Millisecond, hub, handler.Dispatch)
	client.Start(context.Background())

	first := waitAcceptedConn(t, accepted)
	defer first.conn.Close()
	waitForPush(t, first.conn, "connected")

	stopped := make(chan struct{})
	go func() {
		_ = client.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for client.Stop to return")
	}

	if err := first.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Fatalf("set gateway read deadline: %v", err)
	}
	for {
		_, _, err := first.conn.ReadMessage()
		if err != nil {
			break
		}
	}

	select {
	case extra := <-accepted:
		extra.conn.Close()
		t.Fatalf("expected no reconnect after Stop, got connection with auth %q", extra.authorization)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestClientStopBeforeStartMakesStartNoOp(t *testing.T) {
	accepted := make(chan acceptedGatewayConn, 1)
	upgrader := gws.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade gateway connection: %v", err)
			return
		}
		defer conn.Close()
		accepted <- acceptedGatewayConn{conn: conn, authorization: r.Header.Get("Authorization")}
	}))
	defer server.Close()

	hub := ws.NewHub()
	wsCfg := config.WebSocketConfig{
		Enabled:             true,
		MaxMessageSizeBytes: 1 << 20,
		PingIntervalMs:      200,
		WriteTimeoutMs:      1000,
		WriteQueueSize:      8,
		MaxObservesPerConn:  4,
	}
	handler := ws.NewHandler(wsCfg, 50*time.Millisecond, hub, testAuthenticator{})
	client := New(Config{
		URL:              wsURL(server.URL),
		Token:            "dev-token",
		HandshakeTimeout: 500 * time.Millisecond,
		ReconnectMin:     30 * time.Millisecond,
		ReconnectMax:     80 * time.Millisecond,
	}, wsCfg, 50*time.Millisecond, hub, handler.Dispatch)

	stopped := make(chan struct{})
	go func() {
		_ = client.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for Stop before Start to return")
	}

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Start panicked after Stop: %v", r)
			}
		}()
		client.Start(context.Background())
	}()

	select {
	case extra := <-accepted:
		extra.conn.Close()
		t.Fatalf("expected Start to be a no-op after Stop, got connection with auth %q", extra.authorization)
	case <-time.After(150 * time.Millisecond):
	}
}

func waitAcceptedConn(t *testing.T, accepted <-chan acceptedGatewayConn) acceptedGatewayConn {
	t.Helper()
	select {
	case conn := <-accepted:
		return conn
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for gateway connection")
		return acceptedGatewayConn{}
	}
}

func waitForPush(t *testing.T, conn *gws.Conn, pushType string) ws.PushFrame {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			t.Fatalf("set push read deadline: %v", err)
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read push frame: %v", err)
		}
		var meta struct {
			Frame string `json:"frame"`
			Type  string `json:"type"`
		}
		if err := json.Unmarshal(raw, &meta); err != nil {
			t.Fatalf("decode push frame: %v", err)
		}
		if meta.Frame != ws.FramePush || meta.Type != pushType {
			continue
		}
		var frame ws.PushFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatalf("decode push payload: %v", err)
		}
		return frame
	}
	t.Fatalf("timed out waiting for push %s", pushType)
	return ws.PushFrame{}
}

func waitForResponse(t *testing.T, conn *gws.Conn, requestID string) ws.ResponseFrame {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			t.Fatalf("set response read deadline: %v", err)
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read response frame: %v", err)
		}
		var meta struct {
			Frame string `json:"frame"`
			ID    string `json:"id"`
		}
		if err := json.Unmarshal(raw, &meta); err != nil {
			t.Fatalf("decode response frame: %v", err)
		}
		if meta.Frame != ws.FrameResponse || meta.ID != requestID {
			continue
		}
		var frame ws.ResponseFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatalf("decode response payload: %v", err)
		}
		return frame
	}
	t.Fatalf("timed out waiting for response %s", requestID)
	return ws.ResponseFrame{}
}

func wsURL(raw string) string {
	return "ws" + strings.TrimPrefix(raw, "http")
}
