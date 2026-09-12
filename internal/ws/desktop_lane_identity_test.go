package ws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"

	gws "github.com/gorilla/websocket"
)

func TestHubReservedDesktopSurfaceCannotBeClaimedBeforeOrAfterPhysicalLane(t *testing.T) {
	sources := []string{desktopMainClientSource, desktopBTWClientSource, desktopSelectionExplainClientSource}
	for _, physicalFirst := range []bool{false, true} {
		name := "untrusted-first"
		if physicalFirst {
			name = "physical-first"
		}
		t.Run(name, func(t *testing.T) {
			hub := NewHub()
			defer hub.CloseAll(1000, "test complete")
			physical := map[string]*Conn{}
			registerPhysical := func() {
				for _, source := range sources {
					physical[source] = desktopLaneTestConn(hub, source, selectionExplainTestAuth())
					hub.register(physical[source])
				}
			}
			if physicalFirst {
				registerPhysical()
			}
			for _, surfaceID := range sources {
				for _, claimedSource := range []string{"webclient", surfaceID} {
					auth := selectionExplainTestAuth()
					auth.Scope = "openid"
					fake := desktopLaneTestConn(hub, claimedSource, auth)
					fake.SetClientSurfaceID(surfaceID)
					hub.register(fake)
					if !fake.isClosed() {
						t.Fatalf("non-app %s reserved %s", claimedSource, surfaceID)
					}
					if _, exists := hub.webClientKeys[fake]; exists {
						t.Fatal("rejected client entered the generic surface map")
					}
				}
			}
			if !physicalFirst {
				registerPhysical()
			}
			if len(hub.snapshotConnections()) != 3 {
				t.Fatal("untrusted connections displaced a physical lane")
			}
			for source, conn := range physical {
				if conn.isClosed() {
					t.Fatalf("untrusted client closed %s", source)
				}
				if resolved, ok := hub.resolveClientConnection(conn.WebClientTarget()); !ok || resolved != conn {
					t.Fatalf("reverse target for %s resolved outside its physical lane", source)
				}
			}
			if target, state := hub.ResolveDesktopMainTarget(); state != contracts.DesktopMainTargetReady || target.SessionID != physical[desktopMainClientSource].SessionID() {
				t.Fatal("untrusted client changed the default Main target")
			}
		})
	}
}

func TestMainWithoutSurfaceIDRemainsAnUnambiguousSessionTarget(t *testing.T) {
	hub := NewHub()
	defer hub.CloseAll(1000, "test complete")
	main := desktopLaneTestConn(hub, desktopMainClientSource, selectionExplainTestAuth())
	main.SetClientSurfaceID("")
	hub.register(main)
	target, state := hub.ResolveDesktopMainTarget()
	if state != contracts.DesktopMainTargetReady || target.SurfaceID != "" || len(hub.webClientConns) != 0 {
		t.Fatal("a missing Main surfaceId must remain empty and bypass the generic map")
	}
	webAuth := selectionExplainTestAuth()
	webAuth.Scope = "openid"
	web := desktopLaneTestConn(hub, "webclient", webAuth)
	hub.register(web)
	if resolved, ok := hub.resolveClientConnection(target); !ok || resolved != main {
		t.Fatal("same-boundary WebClient replaced the Main session target")
	}
	for _, source := range []string{"webclient", desktopMainClientSource, desktopBTWClientSource} {
		fake := desktopLaneTestConn(hub, source, webAuth)
		fake.SetClientSurfaceID(desktopMainClientSource)
		hub.register(fake)
		if !fake.isClosed() {
			t.Fatal("untrusted client reserved the absent Main surface identity")
		}
		if resolved, ok := hub.resolveClientConnection(target); !ok || resolved != main {
			t.Fatal("reserved-surface spoof changed session-target resolution")
		}
	}
}

func TestPhysicalLaneRejectsOtherOrArbitraryExplicitSurfaceIdentities(t *testing.T) {
	for _, surfaceID := range []string{desktopMainClientSource, desktopBTWClientSource, "ordinary-surface"} {
		if desktopLaneMetadataAuthorized(desktopSelectionExplainClientSource, "device-1", surfaceID, selectionExplainTestAuth()) {
			t.Fatalf("explanation source accepted another surface identity %s", surfaceID)
		}
	}
}

type reservedLaneTestAuthenticator struct{}

func (reservedLaneTestAuthenticator) VerifyToken(ctx context.Context, token string) (AuthSession, error) {
	if token == "app-test-token" {
		auth := selectionExplainTestAuth()
		auth.Context = ctx
		return auth, nil
	}
	// The handshake supplies the same deviceId, but a browser token does not
	// turn that unverified metadata into an app-device credential.
	return AuthSession{Context: ctx, Subject: "user-1", Scope: "openid"}, nil
}

func TestWebSocketHandshakeRejectsReservedIdentityWithoutDisturbingThreeLiveLanes(t *testing.T) {
	hub := NewHub()
	defer hub.CloseAll(1000, "test complete")
	handler := NewHandler(config.WebSocketConfig{WriteQueueSize: 8, PingInterval: 30}, hub, reservedLaneTestAuthenticator{})
	server := httptest.NewServer(handler)
	defer server.Close()
	sources := []string{desktopMainClientSource, desktopBTWClientSource, desktopSelectionExplainClientSource}
	for _, source := range sources {
		query := url.Values{"source": {source}, "surfaceId": {source}, "deviceId": {"device-1"}, "token": {"app-test-token"}}
		conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/?"+query.Encode(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		var connected PushFrame
		if err := conn.ReadJSON(&connected); err != nil || connected.Type != "connected" {
			t.Fatalf("physical handshake: %v %#v", err, connected)
		}
	}
	for _, surfaceID := range sources {
		for _, source := range []string{"webclient", surfaceID} {
			query := url.Values{"source": {source}, "surfaceId": {surfaceID}, "deviceId": {"device-1"}, "token": {"browser-test-token"}}
			conn, response, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/?"+query.Encode(), nil)
			if conn != nil {
				_ = conn.Close()
			}
			if response != nil {
				_ = response.Body.Close()
			}
			if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
				t.Fatalf("forged %s/%s was not denied before registration", source, surfaceID)
			}
		}
	}
	if len(hub.snapshotConnections()) != 3 {
		t.Fatal("forged handshakes replaced a physical connection")
	}
	for _, conn := range hub.snapshotConnections() {
		if conn.isClosed() || !conn.canRegisterClientIdentity() {
			t.Fatal("a trusted physical connection was affected")
		}
	}
}
