package ws

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/config"
	"agent-platform/internal/observability"
)

func TestMonitorConnectionOmitsAbsentOptionalTimes(t *testing.T) {
	encoded, err := json.Marshal(MonitorConnection{
		SessionID:   "ws_1",
		ConnectedAt: 1_700_000_000_000,
	})
	if err != nil {
		t.Fatalf("marshal monitor connection: %v", err)
	}
	for _, field := range []string{"closedAt", "lastSeenAt", "lastMessageAt"} {
		if strings.Contains(string(encoded), `"`+field+`"`) {
			t.Fatalf("expected absent %s to be omitted: %s", field, encoded)
		}
	}
}

func TestHubMonitorDropsMessagesWithoutValidTimestamp(t *testing.T) {
	hub := NewHub()
	hub.recordMonitorMessage(MonitorMessage{SessionID: "ws_bad", Timestamp: 0, Direction: "in"})
	hub.recordMonitorMessage(MonitorMessage{SessionID: "ws_seconds", Timestamp: 1_700_000_000, Direction: "in"})
	if messages := hub.MonitorMessages(10, MonitorFilter{}).Messages; len(messages) != 0 {
		t.Fatalf("expected invalid monitor messages to be dropped, got %#v", messages)
	}

	now := time.Now().UnixMilli()
	hub.recordMonitorMessage(MonitorMessage{SessionID: "ws_valid", Timestamp: now, Direction: "in"})
	messages := hub.MonitorMessages(10, MonitorFilter{}).Messages
	if len(messages) != 1 || messages[0].Timestamp != now {
		t.Fatalf("expected valid monitor message to remain, got %#v", messages)
	}
}

func TestHubMonitorTracksConnectionLifecycle(t *testing.T) {
	hub := NewHub()
	conn := NewConn(nil, hub, config.WebSocketConfig{WriteQueueSize: 4}, time.Second, AuthSession{Subject: "tester"})
	conn.SetClientInfo("192.168.1.42:4815", "monitor-test-agent")
	conn.SetClientMetadata("WebClient", "device-123")

	hub.register(conn)

	overview := hub.MonitorOverview(5)
	if overview.WS.ConnectionCount != 1 {
		t.Fatalf("expected one active connection, got %#v", overview.WS)
	}
	if overview.WS.LatestConnection == nil {
		t.Fatalf("expected latest connection")
	}
	latest := overview.WS.LatestConnection
	if latest.SessionID != conn.SessionID() || !latest.Active || latest.Kind != "client" || latest.Subject != "tester" {
		t.Fatalf("unexpected latest connection: %#v", latest)
	}
	if latest.RemoteAddr != "192.168.1.0" {
		t.Fatalf("expected masked remote address, got %q", latest.RemoteAddr)
	}
	if latest.UserAgent != "monitor-test-agent" {
		t.Fatalf("unexpected user agent: %q", latest.UserAgent)
	}
	if latest.Source != "webclient" || latest.DeviceID != "device-123" {
		t.Fatalf("unexpected client metadata: %#v", latest)
	}

	hub.unregister(conn)

	overview = hub.MonitorOverview(5)
	if overview.WS.ConnectionCount != 0 {
		t.Fatalf("expected no active connections, got %#v", overview.WS)
	}
	if overview.WS.LatestConnection == nil || overview.WS.LatestConnection.Active {
		t.Fatalf("expected latest connection to remain as inactive, got %#v", overview.WS.LatestConnection)
	}
	if overview.WS.LatestConnection.ClosedAt == 0 {
		t.Fatalf("expected closedAt to be populated, got %#v", overview.WS.LatestConnection)
	}

	connections := hub.MonitorConnections(10, MonitorFilter{SessionID: conn.SessionID(), Source: "webclient", DeviceID: "device-123"}).Connections
	if len(connections) != 1 || connections[0].SessionID != conn.SessionID() {
		t.Fatalf("expected session filter to return closed connection, got %#v", connections)
	}
	if filtered := hub.MonitorConnections(10, MonitorFilter{Source: "desktop"}).Connections; len(filtered) != 0 {
		t.Fatalf("expected source filter mismatch, got %#v", filtered)
	}
}

func TestHubMonitorRecordsRecentMessagesAndSanitizesPreview(t *testing.T) {
	hub := NewHub()
	conn := NewConn(nil, hub, config.WebSocketConfig{WriteQueueSize: 8}, time.Second, AuthSession{})
	conn.SetClientMetadata("APP", "device-message")
	hub.register(conn)

	conn.recordOutboundMessage(PushFrame{Frame: FramePush, Type: "heartbeat", Data: map[string]any{"timestamp": 1}})
	conn.recordOutboundMessage(PushFrame{Frame: FramePush, Type: "connected", Data: map[string]any{"sessionId": conn.SessionID(), "token": "secret-value"}})
	raw := []byte(`{"frame":"request","type":"/api/agents","id":"req_1","payload":{"token":"secret-value","message":"` + strings.Repeat("x", 600) + `"}}`)
	conn.recordInboundMessage(raw, RequestFrame{Frame: FrameRequest, Type: "/api/agents", ID: "req_1"}, "")

	messages := hub.MonitorMessages(5, MonitorFilter{}).Messages
	if len(messages) != 2 {
		t.Fatalf("expected heartbeat to be skipped and two messages to remain, got %#v", messages)
	}
	latest := messages[0]
	if latest.Direction != "in" || latest.Frame != FrameRequest || latest.Type != "/api/agents" || latest.ID != "req_1" {
		t.Fatalf("unexpected latest message: %#v", latest)
	}
	if latest.Source != "app" || latest.DeviceID != "device-message" {
		t.Fatalf("unexpected message metadata: %#v", latest)
	}
	if !latest.Truncated || len([]rune(latest.PayloadPreview)) > monitorPreviewMaxRunes {
		t.Fatalf("expected truncated preview capped at %d runes, got %#v", monitorPreviewMaxRunes, latest)
	}
	if strings.Contains(latest.PayloadPreview, "secret-value") || !strings.Contains(latest.PayloadPreview, observability.HiddenToken) {
		t.Fatalf("expected sensitive payload to be hidden, got %q", latest.PayloadPreview)
	}

	connections := hub.MonitorConnections(10, MonitorFilter{SessionID: conn.SessionID()}).Connections
	if len(connections) != 1 {
		t.Fatalf("expected connection snapshot, got %#v", connections)
	}
	if connections[0].ReceivedMessages != 1 || connections[0].SentMessages != 1 || connections[0].Errors != 0 {
		t.Fatalf("unexpected message counters: %#v", connections[0])
	}
	filtered := hub.MonitorMessages(5, MonitorFilter{Source: "app", DeviceID: "device-message"}).Messages
	if len(filtered) != 2 {
		t.Fatalf("expected metadata filters to return two messages, got %#v", filtered)
	}
}

func TestMonitorRedactsTerminalInputPreview(t *testing.T) {
	hub := NewHub()
	conn := NewConn(nil, hub, config.WebSocketConfig{WriteQueueSize: 8}, time.Second, AuthSession{})
	hub.register(conn)
	raw := []byte(`{"frame":"request","type":"/api/terminal/input","id":"term_input","payload":{"terminalId":"term_1","data":"secret-token-value"}}`)
	conn.recordInboundMessage(raw, RequestFrame{
		Frame: FrameRequest,
		Type:  "/api/terminal/input",
		ID:    "term_input",
		Payload: MarshalPayload(map[string]any{
			"terminalId": "term_1",
			"data":       "secret-token-value",
		}),
	}, "")

	messages := hub.MonitorMessages(1, MonitorFilter{}).Messages
	if len(messages) != 1 {
		t.Fatalf("expected one message, got %#v", messages)
	}
	if strings.Contains(messages[0].PayloadPreview, "secret-token-value") {
		t.Fatalf("terminal input leaked in preview: %#v", messages[0])
	}
	if !strings.Contains(messages[0].PayloadPreview, `"dataBytes":18`) {
		t.Fatalf("expected data byte summary, got %#v", messages[0])
	}
}

func TestWSClientMetadataFromRequestNormalizesAndFallsBackToAuthDeviceID(t *testing.T) {
	req := httptest.NewRequest("GET", "/ws?source=WebClient&device_id=device-query", nil)
	source, deviceID := wsClientMetadataFromRequest(req, AuthSession{DeviceID: "device-claim"})
	if source != "webclient" || deviceID != "device-query" {
		t.Fatalf("unexpected query metadata: source=%q deviceId=%q", source, deviceID)
	}

	req = httptest.NewRequest("GET", "/ws?source="+strings.Repeat("A", monitorSourceMaxRunes+5), nil)
	source, deviceID = wsClientMetadataFromRequest(req, AuthSession{DeviceID: strings.Repeat("d", monitorDeviceIDMaxRunes+5)})
	if len([]rune(source)) != monitorSourceMaxRunes || source != strings.Repeat("a", monitorSourceMaxRunes) {
		t.Fatalf("expected normalized and truncated source, got %q", source)
	}
	if len([]rune(deviceID)) != monitorDeviceIDMaxRunes {
		t.Fatalf("expected truncated deviceId, got %q", deviceID)
	}
}
