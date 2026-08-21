package ws

import (
	"context"
	"testing"

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
	auth := AuthSession{
		Context:          context.Background(),
		Subject:          "user-1",
		DeviceID:         "device-1",
		DeviceIDVerified: true,
		Scope:            "app",
	}
	first := NewConn(nil, hub, config.WebSocketConfig{WriteQueueSize: 4}, auth)
	first.SetClientMetadata("desktop-chat", "device-1")
	first.SetClientSurfaceID("surface-1")
	second := NewConn(nil, hub, config.WebSocketConfig{WriteQueueSize: 4}, auth)
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
	conn := NewConn(nil, hub, config.WebSocketConfig{WriteQueueSize: 4}, AuthSession{
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

func TestHubDesktopMainTargetTracksLatestConnectionGeneration(t *testing.T) {
	hub := NewHub()
	if target, state := hub.ResolveDesktopMainTarget(); !target.IsZero() || state != contracts.DesktopMainTargetMissing {
		t.Fatalf("initial desktop main target = %#v state=%q", target, state)
	}

	auth := AuthSession{
		Context:          context.Background(),
		Subject:          "user-1",
		DeviceID:         "device-1",
		DeviceIDVerified: true,
		Scope:            "app",
	}
	first := NewConn(nil, hub, config.WebSocketConfig{WriteQueueSize: 4}, auth)
	first.SetClientMetadata("desktop-main", "device-1")
	hub.register(first)
	firstTarget, state := hub.ResolveDesktopMainTarget()
	if state != contracts.DesktopMainTargetReady || firstTarget.SessionID != first.SessionID() {
		t.Fatalf("first desktop main target = %#v state=%q", firstTarget, state)
	}
	first.UpdateAuth(AuthSession{Context: context.Background(), Subject: "user-1", DeviceID: "device-1", DeviceIDVerified: true, Scope: "openid"})
	if target, currentState := hub.ResolveDesktopMainTarget(); !target.IsZero() || currentState != contracts.DesktopMainTargetDisconnected {
		t.Fatalf("invalid refreshed identity remained default = %#v state=%q", target, currentState)
	}
	first.UpdateAuth(auth)
	if target, currentState := hub.ResolveDesktopMainTarget(); currentState != contracts.DesktopMainTargetReady || target.SessionID != first.SessionID() {
		t.Fatalf("valid refreshed identity did not recover default = %#v state=%q", target, currentState)
	}

	second := NewConn(nil, hub, config.WebSocketConfig{WriteQueueSize: 4}, auth)
	second.SetClientMetadata("DESKTOP-MAIN", "device-1")
	hub.register(second)
	secondTarget, state := hub.ResolveDesktopMainTarget()
	if state != contracts.DesktopMainTargetReady || secondTarget.SessionID != second.SessionID() {
		t.Fatalf("replacement desktop main target = %#v state=%q", secondTarget, state)
	}
	if !first.isClosed() {
		t.Fatal("replaced desktop main connection must close")
	}

	// A late unregister from the old generation cannot clear the replacement.
	hub.unregister(first)
	if target, currentState := hub.ResolveDesktopMainTarget(); currentState != contracts.DesktopMainTargetReady || target.SessionID != second.SessionID() {
		t.Fatalf("late old-generation unregister changed target = %#v state=%q", target, currentState)
	}

	hub.unregister(second)
	if target, currentState := hub.ResolveDesktopMainTarget(); !target.IsZero() || currentState != contracts.DesktopMainTargetDisconnected {
		t.Fatalf("disconnected desktop main target = %#v state=%q", target, currentState)
	}
}

func TestHubDesktopMainTargetRequiresDesktopSourceAndDevice(t *testing.T) {
	tests := []struct {
		name             string
		source           string
		deviceID         string
		authDeviceID     string
		deviceIDVerified bool
		scope            string
	}{
		{name: "other source", source: "desktop-chat", deviceID: "device-1", authDeviceID: "device-1", deviceIDVerified: true, scope: "app"},
		{name: "missing device", source: "desktop-main", deviceID: "", authDeviceID: "device-1", deviceIDVerified: true, scope: "app"},
		{name: "unverified device", source: "desktop-main", deviceID: "device-1", authDeviceID: "device-1", scope: "app"},
		{name: "wrong scope", source: "desktop-main", deviceID: "device-1", authDeviceID: "device-1", deviceIDVerified: true, scope: "openid"},
		{name: "device mismatch", source: "desktop-main", deviceID: "device-query", authDeviceID: "device-token", deviceIDVerified: true, scope: "app"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hub := NewHub()
			conn := NewConn(nil, hub, config.WebSocketConfig{WriteQueueSize: 4}, AuthSession{
				Context:          context.Background(),
				DeviceID:         test.authDeviceID,
				DeviceIDVerified: test.deviceIDVerified,
				Scope:            test.scope,
			})
			conn.SetClientMetadata(test.source, test.deviceID)
			hub.register(conn)
			if target, state := hub.ResolveDesktopMainTarget(); !target.IsZero() || state != contracts.DesktopMainTargetMissing {
				t.Fatalf("unexpected desktop main target = %#v state=%q", target, state)
			}
		})
	}
}

func TestHubWebClientSurfaceDoesNotCrossSubjects(t *testing.T) {
	hub := NewHub()
	conn := NewConn(nil, hub, config.WebSocketConfig{WriteQueueSize: 4}, AuthSession{
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
	conn := NewConn(nil, hub, config.WebSocketConfig{WriteQueueSize: 4}, AuthSession{
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
	first := NewConn(nil, hub, config.WebSocketConfig{WriteQueueSize: 4}, AuthSession{Context: ctx, Subject: "peer-a"})
	first.SetClientInfo("127.0.0.1:1000", "peer-agent/1")
	second := NewConn(nil, hub, config.WebSocketConfig{WriteQueueSize: 4}, AuthSession{Context: ctx, Subject: "peer-b"})
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
