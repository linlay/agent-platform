package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/catalog"
	channelpkg "agent-platform-runner-go/internal/channel"
	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/ws"

	gws "github.com/gorilla/websocket"
)

func TestWebSocketChannelsAndAgentsRespectChannelConfig(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.Enabled = true
			cfg.WebSocket.WriteQueueSize = 4
			cfg.WebSocket.PingIntervalMs = 30000
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
			ID:           "wecom",
			Name:         "WeCom",
			Type:         config.ChannelTypeBridge,
			DefaultAgent: "assistant",
			AllAgents:    false,
			Agents:       []string{"assistant", "code-helper"},
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
	var channelsFrame ws.ResponseFrame
	if err := conn.ReadJSON(&channelsFrame); err != nil {
		t.Fatalf("read channels frame: %v", err)
	}
	if channelsFrame.Frame != ws.FrameResponse || channelsFrame.ID != "req_channels" {
		t.Fatalf("unexpected channels frame: %#v", channelsFrame)
	}
	channelsData, err := marshalResponseData[[]api.ChannelSummary](channelsFrame.Data)
	if err != nil {
		t.Fatalf("decode channels data: %v", err)
	}
	if len(channelsData) != 1 || !channelsData[0].Connected {
		t.Fatalf("unexpected channels payload: %#v", channelsData)
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
	if len(agentsData) != 2 || agentsData[0].Key != "assistant" || agentsData[1].Key != "code-helper" {
		t.Fatalf("unexpected filtered agents payload: %#v", agentsData)
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
