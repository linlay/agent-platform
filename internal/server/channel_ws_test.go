package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	channelpkg "agent-platform/internal/channel"
	"agent-platform/internal/config"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

type channelSessionObserverStub struct {
	opened chan *ws.Conn
	pushes chan ws.PushFrame
	closed chan *ws.Conn
}

func (s *channelSessionObserverStub) ChannelConnected(_ string, conn *ws.Conn, _ time.Duration) {
	s.opened <- conn
}

func (s *channelSessionObserverStub) ChannelPush(_ string, _ *ws.Conn, push ws.PushFrame) {
	s.pushes <- push
}

func (s *channelSessionObserverStub) ChannelDisconnected(_ string, conn *ws.Conn) {
	s.closed <- conn
}

func TestWebSocketChannelsRouteRemovedAndAgentsIgnoreChannelFilter(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 4
			cfg.WebSocket.PingInterval = 30000
		},
	})

	fixture.server.deps.Registry = channelTestCatalogRegistry{
		defaultAgent: "assistant",
		agents: []api.AgentSummary{
			{Key: "assistant", Name: "Assistant"},
			{Key: "code-helper", Name: "Code Helper"},
			{Key: "customer-service", Name: "Customer Service"},
		},
		defs: map[string]catalog.AgentDefinition{
			"assistant":        {Key: "assistant", Name: "Assistant", ModelKey: "mock-model"},
			"code-helper":      {Key: "code-helper", Name: "Code Helper", ModelKey: "mock-model"},
			"customer-service": {Key: "customer-service", Name: "Customer Service", ModelKey: "mock-model"},
		},
	}
	fixture.server.deps.Channels = channelpkg.NewRegistry([]config.ChannelConfig{
		{
			ID:       "wecom",
			Name:     "WeCom",
			Mode:     config.ChannelModeClient,
			Endpoint: config.ChannelEndpointConfig{URL: "ws://wecom.example/ws"},
		},
	})
	fixture.server.deps.ChannelStatus = channelStatusStub{"wecom": true}

	server := httptest.NewServer(fixture.server)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	readConnectedPush(t, conn)

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/channels",
		ID:    "req_channels",
	}); err != nil {
		t.Fatalf("write channels request: %v", err)
	}
	var channelsFrame ws.ErrorFrame
	if err := conn.ReadJSON(&channelsFrame); err != nil {
		t.Fatalf("read channels frame: %v", err)
	}
	if channelsFrame.Frame != ws.FrameError || channelsFrame.Type != "invalid_request" || channelsFrame.ID != "req_channels" ||
		channelsFrame.Code != http.StatusBadRequest || !strings.Contains(channelsFrame.Msg, "unknown type: /api/channels") {
		t.Fatalf("unexpected channels frame: %#v", channelsFrame)
	}

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/agents",
		ID:    "req_agents",
		Payload: ws.MarshalPayload(map[string]any{
			"channel": "wecom",
		}),
	}); err != nil {
		t.Fatalf("write agents request: %v", err)
	}
	var agentsFrame ws.ResponseFrame
	if err := conn.ReadJSON(&agentsFrame); err != nil {
		t.Fatalf("read agents frame: %v", err)
	}
	agentsData, err := marshalResponseData[[]api.AgentSummary](agentsFrame.Data)
	if err != nil {
		t.Fatalf("decode agents data: %v", err)
	}
	if len(agentsData) != 3 || agentsData[0].Key != "assistant" || agentsData[1].Key != "code-helper" || agentsData[2].Key != "customer-service" {
		t.Fatalf("unexpected agents payload: %#v", agentsData)
	}

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/agents",
		ID:    "req_agents_invalid",
		Payload: ws.MarshalPayload(map[string]any{
			"includeChats": 51,
		}),
	}); err != nil {
		t.Fatalf("write invalid agents request: %v", err)
	}
	var invalidFrame ws.ResponseFrame
	if err := conn.ReadJSON(&invalidFrame); err != nil {
		t.Fatalf("read invalid agents frame: %v", err)
	}
	if invalidFrame.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid includeChats to fail, got %#v", invalidFrame)
	}

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/agents",
		ID:    "req_agents_invalid_scope",
		Payload: ws.MarshalPayload(map[string]any{
			"scope": "missing",
		}),
	}); err != nil {
		t.Fatalf("write invalid scope agents request: %v", err)
	}
	var invalidScopeFrame ws.ResponseFrame
	if err := conn.ReadJSON(&invalidScopeFrame); err != nil {
		t.Fatalf("read invalid scope agents frame: %v", err)
	}
	if invalidScopeFrame.Code != http.StatusBadRequest || invalidScopeFrame.Type != "invalid_request" {
		t.Fatalf("expected invalid scope to fail, got %#v", invalidScopeFrame)
	}
}

func TestChannelWebSocketRequiresServerModeChannel(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 4
			cfg.WebSocket.PingInterval = 30000
			cfg.Channels = []config.ChannelConfig{
				{
					ID:   "public-entry",
					Mode: config.ChannelModeServer,
					Endpoint: config.ChannelEndpointConfig{
						Path: "/ws/channel",
					},
				},
				{
					ID:   "peer-a",
					Mode: config.ChannelModeClient,
				},
			}
		},
	})
	server := httptest.NewServer(fixture.server)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/channel?channelId=public-entry"
	conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial channel websocket: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("channel websocket must not send the ordinary client connected push")
	}
	_ = conn.Close()

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ws/channel", nil))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "channelId is required") {
		t.Fatalf("expected missing channelId to fail, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ws/channel?channelId=peer-a", nil))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "server mode") {
		t.Fatalf("expected client-mode channel to be rejected, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestServerChannelForwardsPeerConnectedPushToSessionObserver(t *testing.T) {
	observer := &channelSessionObserverStub{opened: make(chan *ws.Conn, 1), pushes: make(chan ws.PushFrame, 1), closed: make(chan *ws.Conn, 1)}
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 8
			cfg.WebSocket.PingInterval = 30
			cfg.Channels = []config.ChannelConfig{{ID: "public-entry", Mode: config.ChannelModeServer, Endpoint: config.ChannelEndpointConfig{Path: "/ws/channel"}}}
		},
	})
	fixture.server.deps.ChannelSessions = observer
	fixture.server.wsHandler = fixture.server.newWSHandler(fixture.server.deps.Notifications.(*ws.Hub))
	server := httptest.NewServer(fixture.server)
	defer server.Close()
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws/channel?channelId=public-entry", nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-observer.opened:
	case <-time.After(time.Second):
		t.Fatal("channel session open was not observed")
	}
	push := ws.PushFrame{Frame: ws.FramePush, Type: "connected", Data: map[string]any{"sessionId": "remote-session"}}
	if err := conn.WriteJSON(push); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-observer.pushes:
		if got.Type != "connected" {
			t.Fatalf("unexpected push %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("channel connected push was not observed")
	}
	_ = conn.Close()
	select {
	case <-observer.closed:
	case <-time.After(time.Second):
		t.Fatal("channel session close was not observed")
	}
}

func readConnectedPush(t *testing.T, conn *gws.Conn) {
	t.Helper()
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read initial ws frame: %v", err)
	}
	var push ws.PushFrame
	if err := json.Unmarshal(raw, &push); err != nil {
		t.Fatalf("decode initial ws frame: %v", err)
	}
	if push.Frame != ws.FramePush || push.Type != "connected" {
		t.Fatalf("unexpected initial ws frame: %s", string(raw))
	}
}

func marshalResponseData[T any](value any) (T, error) {
	var out T
	data, err := json.Marshal(value)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, err
	}
	return out, nil
}
