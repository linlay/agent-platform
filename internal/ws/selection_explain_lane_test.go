package ws

import (
	"context"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

func selectionExplainTestAuth() AuthSession {
	return AuthSession{
		Context: context.Background(), Subject: "user-1", DeviceID: "device-1",
		DeviceIDVerified: true, Scope: "app",
	}
}

func desktopLaneTestConn(hub *Hub, source string, auth AuthSession) *Conn {
	conn := NewConn(nil, hub, config.WebSocketConfig{WriteQueueSize: 4}, auth)
	conn.SetClientMetadata(source, "device-1")
	conn.SetClientSurfaceID(source)
	return conn
}

func TestDesktopSelectionExplainRequiresAuthenticatedAppDevice(t *testing.T) {
	for _, test := range []struct {
		name    string
		source  string
		change  func(*AuthSession)
		allowed bool
	}{
		{name: "verified app", source: desktopSelectionExplainClientSource, allowed: true},
		{name: "ordinary webclient", source: "webclient"},
		{name: "wrong scope", source: desktopSelectionExplainClientSource, change: func(auth *AuthSession) { auth.Scope = "openid" }},
		{name: "unverified device", source: desktopSelectionExplainClientSource, change: func(auth *AuthSession) { auth.DeviceIDVerified = false }},
		{name: "wrong device", source: desktopSelectionExplainClientSource, change: func(auth *AuthSession) { auth.DeviceID = "another-device" }},
		{name: "missing device", source: desktopSelectionExplainClientSource, change: func(auth *AuthSession) { auth.DeviceID = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			auth := selectionExplainTestAuth()
			if test.change != nil {
				test.change(&auth)
			}
			hub := NewHub()
			conn := desktopLaneTestConn(hub, test.source, auth)
			defer conn.close(1000, "test complete")
			hub.register(conn)
			if got := conn.IsDesktopSelectionExplain(); got != test.allowed {
				t.Fatalf("selection explanation lane authorization = %v, want %v", got, test.allowed)
			}
			if (hub.desktopSelectionExplainConn == conn) != test.allowed {
				t.Fatal("source metadata registered an unauthorized explanation lane")
			}
			if conn.IsDesktopBTW() {
				t.Fatal("the explanation connection must remain distinct from desktop-btw")
			}
			if _, state := hub.ResolveDesktopMainTarget(); state != contracts.DesktopMainTargetMissing {
				t.Fatalf("explanation source became the default Desktop Main target: %s", state)
			}
		})
	}
}

func TestDesktopSelectionExplainPreservesExplicitAuthDisabledDevelopmentMode(t *testing.T) {
	conn := desktopLaneTestConn(nil, desktopSelectionExplainClientSource, AuthSession{
		Context: context.Background(), DeviceID: "device-1", AuthDisabled: true,
	})
	defer conn.close(1000, "test complete")
	if !conn.IsDesktopSelectionExplain() {
		t.Fatal("explicit auth-disabled development mode must preserve lane access")
	}
	conn.SetClientMetadata(desktopSelectionExplainClientSource, "another-device")
	if conn.IsDesktopSelectionExplain() {
		t.Fatal("development mode must still match the connection device")
	}
}

func TestHubDesktopThreePhysicalLanesReplaceOnlyTheirOwnGeneration(t *testing.T) {
	hub := NewHub()
	defer hub.CloseAll(1000, "test complete")
	sources := []string{desktopMainClientSource, desktopBTWClientSource, desktopSelectionExplainClientSource}
	current := make(map[string]*Conn)
	for _, source := range sources {
		current[source] = desktopLaneTestConn(hub, source, selectionExplainTestAuth())
		hub.register(current[source])
	}
	for _, source := range sources {
		previous := current[source]
		replacement := desktopLaneTestConn(hub, source, selectionExplainTestAuth())
		hub.register(replacement)
		current[source] = replacement
		if !previous.isClosed() || replacement.isClosed() {
			t.Fatalf("%s did not replace only its prior connection", source)
		}
		// A late close of the old generation must not unregister its replacement.
		hub.unregister(previous)
		if len(hub.snapshotConnections()) != 3 {
			t.Fatal("three independent physical lanes must coexist")
		}
		for _, active := range current {
			if active.isClosed() {
				t.Fatal("replacing one Desktop lane closed another")
			}
		}
		if hub.desktopMainConn != current[desktopMainClientSource] ||
			hub.desktopBTWConn != current[desktopBTWClientSource] ||
			hub.desktopSelectionExplainConn != current[desktopSelectionExplainClientSource] {
			t.Fatal("lane registry changed another lane's generation")
		}
		if target, state := hub.ResolveDesktopMainTarget(); state != contracts.DesktopMainTargetReady ||
			target.SessionID != current[desktopMainClientSource].SessionID() {
			t.Fatal("an auxiliary lane changed the default Desktop Main target")
		}
	}
	hub.Broadcast("run.finished", map[string]any{"runId": "run-1"})
	if len(current[desktopMainClientSource].writeQueue) != 1 {
		t.Fatal("Main must still receive global Push")
	}
	if len(current[desktopBTWClientSource].writeQueue) != 0 || len(current[desktopSelectionExplainClientSource].writeQueue) != 0 {
		t.Fatal("an auxiliary Desktop lane received global Push")
	}
	current[desktopSelectionExplainClientSource].close(1000, "explanation closed")
	if hub.desktopSelectionExplainConn != nil || current[desktopMainClientSource].isClosed() || current[desktopBTWClientSource].isClosed() {
		t.Fatal("closing the explanation lane affected Main Chat or WorkPanel BTW")
	}
}
