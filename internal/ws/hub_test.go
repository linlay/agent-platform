package ws

import (
	"context"
	"testing"
	"time"

	"agent-platform/internal/config"

	gws "github.com/gorilla/websocket"
)

func TestHubBroadcast(t *testing.T) {
	hub := NewHub()
	conn := &Conn{
		writeQueue: make(chan outboundMessage, 1),
		closed:     make(chan struct{}),
	}
	hub.register(conn)

	hub.Broadcast("catalog.updated", map[string]any{"reason": "agents"})

	msg := <-conn.writeQueue
	push, ok := msg.frame.(PushFrame)
	if !ok {
		t.Fatalf("expected push frame, got %T", msg.frame)
	}
	if push.Type != "catalog.updated" {
		t.Fatalf("unexpected push type: %#v", push)
	}
}

func TestHubCloseAllClosesRegisteredConnections(t *testing.T) {
	hub := NewHub()
	first := &Conn{
		hub:        hub,
		writeQueue: make(chan outboundMessage, 1),
		closed:     make(chan struct{}),
	}
	second := &Conn{
		hub:        hub,
		writeQueue: make(chan outboundMessage, 1),
		closed:     make(chan struct{}),
	}
	hub.register(first)
	hub.register(second)

	hub.CloseAll(gws.CloseNormalClosure, "server shutting down")

	if !first.isClosed() {
		t.Fatalf("expected first connection to close")
	}
	if !second.isClosed() {
		t.Fatalf("expected second connection to close")
	}
	if got := len(hub.conns); got != 0 {
		t.Fatalf("expected hub to be empty after CloseAll, got %d", got)
	}
}

func TestHubGatewayConnectionUsesLatestAndFallsBack(t *testing.T) {
	hub := NewHub()
	ctx := WithGatewayContext(context.Background(), GatewayContext{Channel: "public-entry"})
	first := &Conn{
		auth:       AuthSession{Context: ctx},
		writeQueue: make(chan outboundMessage, 1),
		closed:     make(chan struct{}),
	}
	second := &Conn{
		auth:       AuthSession{Context: ctx},
		writeQueue: make(chan outboundMessage, 1),
		closed:     make(chan struct{}),
	}

	hub.register(first)
	if got, ok := hub.GatewayConnection("public-entry"); !ok || got != first {
		t.Fatalf("expected first gateway connection, got %#v ok=%v", got, ok)
	}

	hub.register(second)
	if got, ok := hub.GatewayConnection("public-entry"); !ok || got != second {
		t.Fatalf("expected latest gateway connection, got %#v ok=%v", got, ok)
	}

	hub.unregister(second)
	if got, ok := hub.GatewayConnection("public-entry"); !ok || got != first {
		t.Fatalf("expected fallback gateway connection, got %#v ok=%v", got, ok)
	}

	hub.unregister(first)
	if got, ok := hub.GatewayConnection("public-entry"); ok || got != nil {
		t.Fatalf("expected no gateway connection, got %#v ok=%v", got, ok)
	}
}

func TestHubGatewayConnectionsReturnsActiveSnapshots(t *testing.T) {
	hub := NewHub()
	ctx := WithGatewayContext(context.Background(), GatewayContext{
		ID:      "public-entry",
		Channel: "public-entry",
	})
	first := NewConn(nil, hub, config.WebSocketConfig{WriteQueueSize: 4}, time.Second, AuthSession{Context: ctx, Subject: "peer-a"})
	first.SetClientInfo("127.0.0.1:1000", "peer-agent/1")
	second := NewConn(nil, hub, config.WebSocketConfig{WriteQueueSize: 4}, time.Second, AuthSession{Context: ctx, Subject: "peer-b"})
	second.SetClientInfo("127.0.0.1:1001", "peer-agent/2")

	hub.register(first)
	hub.register(second)

	snapshots := hub.GatewayConnections("public-entry")
	if len(snapshots) != 2 {
		t.Fatalf("expected two active gateway snapshots, got %#v", snapshots)
	}
	if snapshots[0].SessionID != second.SessionID() || snapshots[1].SessionID != first.SessionID() {
		t.Fatalf("expected latest connection first, got %#v", snapshots)
	}
	if snapshots[0].Channel != "public-entry" || snapshots[0].GatewayID != "public-entry" || !snapshots[0].Active {
		t.Fatalf("unexpected latest snapshot: %#v", snapshots[0])
	}

	hub.unregister(second)
	snapshots = hub.GatewayConnections("public-entry")
	if len(snapshots) != 1 || snapshots[0].SessionID != first.SessionID() {
		t.Fatalf("expected first connection after unregister, got %#v", snapshots)
	}
}
