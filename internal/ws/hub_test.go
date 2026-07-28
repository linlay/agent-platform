package ws

import (
	"context"
	"testing"
	"time"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"

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

func TestHubWebClientSurfaceReplacesOldConnection(t *testing.T) {
	hub := NewHub()
	auth := AuthSession{Context: context.Background(), Subject: "user-1", DeviceID: "device-1"}
	first := NewConn(nil, hub, config.WebSocketConfig{WriteQueueSize: 4}, time.Second, auth)
	first.SetClientMetadata("desktop-chat", "device-1")
	first.SetClientSurfaceID("surface-1")
	second := NewConn(nil, hub, config.WebSocketConfig{WriteQueueSize: 4}, time.Second, auth)
	second.SetClientMetadata("desktop-copilot", "device-1")
	second.SetClientSurfaceID("surface-1")

	hub.register(first)
	target := contracts.WebClientTarget{
		BoundaryKey: "subject:user-1\x00device:device-1",
		Subject:     "user-1",
		SurfaceID:   "surface-1",
	}
	if got, ok := hub.resolveWebClientConnection(target); !ok || got != first {
		t.Fatalf("expected first webclient connection, got %#v ok=%v", got, ok)
	}

	hub.register(second)
	if got, ok := hub.resolveWebClientConnection(target); !ok || got != second {
		t.Fatalf("expected latest webclient connection, got %#v ok=%v", got, ok)
	}
	if !first.isClosed() {
		t.Fatal("expected replaced webclient connection to close")
	}

	hub.unregister(second)
	if got, ok := hub.resolveWebClientConnection(target); ok || got != nil {
		t.Fatalf("expected no webclient connection, got %#v ok=%v", got, ok)
	}
}

func TestHubWebClientSessionTargetDoesNotRequireSurfaceOrSource(t *testing.T) {
	hub := NewHub()
	conn := NewConn(nil, hub, config.WebSocketConfig{WriteQueueSize: 4}, time.Second, AuthSession{
		Context:  context.Background(),
		Subject:  "user-1",
		DeviceID: "device-1",
	})
	hub.register(conn)

	target := conn.WebClientTarget()
	if target.SessionID != conn.SessionID() || target.IsZero() {
		t.Fatalf("expected direct session target, got %#v", target)
	}
	if target.SurfaceID != "" {
		t.Fatalf("expected no logical surface, got %#v", target)
	}
	if len(hub.webClientConns) != 0 || len(hub.webClientKeys) != 0 {
		t.Fatalf("connection without surface must not enter logical surface map")
	}
	if got, ok := hub.resolveWebClientConnection(target); !ok || got != conn {
		t.Fatalf("expected direct session resolution, got %#v ok=%v", got, ok)
	}
}

func TestHubWebClientSurfaceDoesNotCrossSubjects(t *testing.T) {
	hub := NewHub()
	conn := NewConn(nil, hub, config.WebSocketConfig{WriteQueueSize: 4}, time.Second, AuthSession{
		Context:  context.Background(),
		Subject:  "user-1",
		DeviceID: "device-1",
	})
	conn.SetClientMetadata("desktop-chat", "device-1")
	conn.SetClientSurfaceID("shared-surface")
	hub.register(conn)

	if got, ok := hub.resolveWebClientConnection(contracts.WebClientTarget{
		BoundaryKey: "subject:user-2\x00device:device-1",
		Subject:     "user-2",
		SurfaceID:   "shared-surface",
	}); ok || got != nil {
		t.Fatalf("expected subject boundary isolation, got %#v ok=%v", got, ok)
	}
}

func TestHubWebClientSurfaceDoesNotCrossDevices(t *testing.T) {
	hub := NewHub()
	conn := NewConn(nil, hub, config.WebSocketConfig{WriteQueueSize: 4}, time.Second, AuthSession{
		Context:  context.Background(),
		Subject:  "user-1",
		DeviceID: "device-1",
	})
	conn.SetClientMetadata("", "device-1")
	conn.SetClientSurfaceID("shared-surface")
	hub.register(conn)

	if got, ok := hub.resolveWebClientConnection(contracts.WebClientTarget{
		BoundaryKey: "subject:user-1\x00device:device-2",
		Subject:     "user-1",
		SurfaceID:   "shared-surface",
	}); ok || got != nil {
		t.Fatalf("expected device boundary isolation, got %#v ok=%v", got, ok)
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
