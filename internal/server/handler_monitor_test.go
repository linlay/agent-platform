package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/config"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

func TestMonitorEndpointsExposeWebSocketSnapshot(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 8
			cfg.WebSocket.PingInterval = 30000
		},
	})

	server := httptest.NewServer(fixture.server)
	defer server.Close()

	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws?source=WebClient&deviceId=device-123", nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	var connected ws.PushFrame
	if err := conn.ReadJSON(&connected); err != nil {
		t.Fatalf("read connected push: %v", err)
	}
	data, _ := connected.Data.(map[string]any)
	sessionID, _ := data["sessionId"].(string)
	if connected.Frame != ws.FramePush || connected.Type != "connected" || sessionID == "" {
		t.Fatalf("unexpected connected push: %#v", connected)
	}

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    "/api/agents",
		ID:      "monitor_req_agents",
		Payload: ws.MarshalPayload(map[string]any{}),
	}); err != nil {
		t.Fatalf("write websocket request: %v", err)
	}
	var response ws.ResponseFrame
	if err := conn.ReadJSON(&response); err != nil {
		t.Fatalf("read websocket response: %v", err)
	}
	if response.Frame != ws.FrameResponse || response.Type != "/api/agents" || response.ID != "monitor_req_agents" {
		t.Fatalf("unexpected websocket response: %#v", response)
	}

	overview := waitForMonitorOverview(t, server.URL, "/api/agents")
	if overview.WS.ConnectionCount != 1 {
		t.Fatalf("expected one websocket connection, got %#v", overview)
	}
	if overview.WS.LatestConnection == nil || overview.WS.LatestConnection.SessionID != sessionID {
		t.Fatalf("expected latest connection for %s, got %#v", sessionID, overview.WS.LatestConnection)
	}
	if overview.WS.LatestConnection.Source != "webclient" || overview.WS.LatestConnection.DeviceID != "device-123" {
		t.Fatalf("unexpected latest connection metadata: %#v", overview.WS.LatestConnection)
	}
	if len(overview.WS.RecentMessages) == 0 {
		t.Fatalf("expected recent websocket messages, got %#v", overview)
	}
	if overview.WS.RecentMessages[0].Source != "webclient" || overview.WS.RecentMessages[0].DeviceID != "device-123" {
		t.Fatalf("unexpected recent message metadata: %#v", overview.WS.RecentMessages[0])
	}

	connections := getMonitorData[ws.MonitorConnectionsSnapshot](t, server.URL+"/api/monitor/ws/connections?limit=1&sessionId="+sessionID+"&source=webclient&deviceId=device-123")
	if connections.ConnectionCount != 1 || len(connections.Connections) != 1 {
		t.Fatalf("expected filtered connection snapshot, got %#v", connections)
	}
	if connections.Connections[0].SessionID != sessionID || !connections.Connections[0].Active {
		t.Fatalf("unexpected filtered connection: %#v", connections.Connections)
	}

	messages := getMonitorData[ws.MonitorMessagesSnapshot](t, server.URL+"/api/monitor/ws/messages?limit=5&sessionId="+sessionID+"&source=webclient&deviceId=device-123")
	if len(messages.Messages) == 0 {
		t.Fatalf("expected filtered messages, got %#v", messages)
	}
	for _, msg := range messages.Messages {
		if msg.SessionID != sessionID || msg.Source != "webclient" || msg.DeviceID != "device-123" {
			t.Fatalf("expected only messages for %s, got %#v", sessionID, messages.Messages)
		}
	}
	mismatched := getMonitorData[ws.MonitorMessagesSnapshot](t, server.URL+"/api/monitor/ws/messages?limit=5&source=desktop")
	if len(mismatched.Messages) != 0 {
		t.Fatalf("expected mismatched source filter to return no messages, got %#v", mismatched)
	}
}

func TestMonitorEndpointsValidateLimits(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{notifications: ws.NewHub()})
	server := httptest.NewServer(fixture.server)
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/monitor/ws/messages?limit=0")
	if err != nil {
		t.Fatalf("get monitor messages: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid limit, got %d", resp.StatusCode)
	}
}

func TestMonitorEndpointsBypassHTTPAuth(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			_, publicKeyPath := writeTestJWTKeyPair(t, t.TempDir())
			cfg.Auth = config.AuthConfig{
				Enabled:            true,
				LocalPublicKeyFile: publicKeyPath,
				Issuer:             "agent-platform-local",
			}
		},
	})
	server := httptest.NewServer(fixture.server)
	defer server.Close()

	for _, path := range []string{
		"/api/monitor",
		"/api/monitor/channels",
		"/api/monitor/ws/connections",
		"/api/monitor/ws/messages",
	} {
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatalf("get %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected %s to bypass auth with 200, got %d", path, resp.StatusCode)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/api/query", strings.NewReader(`{"message":"鉴权测试"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected non-monitor API to require auth, got %d: %s", rec.Code, rec.Body.String())
	}
}

func waitForMonitorOverview(t *testing.T, baseURL string, messageType string) ws.MonitorOverview {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		overview := getMonitorData[ws.MonitorOverview](t, baseURL+"/api/monitor")
		for _, msg := range overview.WS.RecentMessages {
			if msg.Type == messageType {
				return overview
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for monitor message type %s", messageType)
	return ws.MonitorOverview{}
}

func getMonitorData[T any](t *testing.T, url string) T {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get %s status=%d", url, resp.StatusCode)
	}
	var envelope struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data T      `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
	if envelope.Code != 0 {
		t.Fatalf("unexpected envelope from %s: %#v", url, envelope)
	}
	return envelope.Data
}
