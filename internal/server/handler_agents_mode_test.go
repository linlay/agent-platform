package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

func TestAgentsModeFiltersHTTPAndWebSocket(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 8
			cfg.WebSocket.PingInterval = 30000
		},
		setupRuntime: func(_ string, cfg *config.Config) {
			for key, body := range map[string]string{
				"plan-agent": strings.Join([]string{
					"key: plan-agent",
					"name: Plan Agent",
					"mode: PLAN_EXECUTE",
					"modelConfig:",
					"  modelKey: mock-model",
				}, "\n"),
				"proxy-agent": strings.Join([]string{
					"key: proxy-agent",
					"name: Proxy Agent",
					"mode: PROXY",
					"modelConfig:",
					"  modelKey: mock-model",
					"visibility:",
					"  scopes:",
					"    - invoke",
				}, "\n"),
			} {
				dir := filepath.Join(cfg.Paths.AgentsDir, key)
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("mkdir %s: %v", key, err)
				}
				if err := os.WriteFile(filepath.Join(dir, "agent.yml"), []byte(body), 0o644); err != nil {
					t.Fatalf("write %s: %v", key, err)
				}
			}
		},
	})
	store, ok := fixture.chats.(*chat.FileStore)
	if !ok {
		t.Fatalf("expected file chat store, got %T", fixture.chats)
	}
	seedAgentModeChat(t, store, "chat-react-agent", "loyw3v28", "mock-agent", "", "agent", "REACT", 1_000)

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents?scope=nav&mode=react,unknown&mode=PLAN_EXECUTE&includeChats=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("mode-filtered agents status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[[]api.AgentSummary]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode mode-filtered agents response: %v", err)
	}
	if len(response.Data) != 2 {
		t.Fatalf("expected REACT and PLAN-EXECUTE agents, got %#v", response.Data)
	}
	byKey := make(map[string]api.AgentSummary, len(response.Data))
	for _, item := range response.Data {
		byKey[item.Key] = item
	}
	if byKey["mock-agent"].Mode != "REACT" || byKey["plan-agent"].Mode != "PLAN-EXECUTE" {
		t.Fatalf("unexpected mode-filtered agents: %#v", response.Data)
	}
	if len(byKey["mock-agent"].Chats) != 1 || byKey["mock-agent"].Chats[0].Mode != "REACT" {
		t.Fatalf("includeChats should remain keyed by agent and expose chat mode, got %#v", byKey["mock-agent"].Chats)
	}
	if strings.Contains(rec.Body.String(), `"agentMode"`) {
		t.Fatalf("agents response must not expose agentMode: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents?scope=invoke&mode=proxy", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("scope+mode agents status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode scope+mode agents response: %v", err)
	}
	if len(response.Data) != 1 || response.Data[0].Key != "proxy-agent" {
		t.Fatalf("scope and mode should combine with AND, got %#v", response.Data)
	}

	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents?mode=TEAM", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("TEAM mode status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode TEAM response: %v", err)
	}
	if len(response.Data) != 0 {
		t.Fatalf("TEAM must not appear in ordinary agent catalog: %#v", response.Data)
	}

	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents?agentMode=REACT", nil))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "use mode instead") {
		t.Fatalf("deprecated agentMode should be rejected, status=%d body=%s", rec.Code, rec.Body.String())
	}

	httpServer := httptest.NewServer(fixture.server)
	defer httpServer.Close()
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()
	readConnectedPush(t, conn)
	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/agents",
		ID:    "agents_mode_ws",
		Payload: ws.MarshalPayload(map[string]any{
			"scope":        "nav",
			"mode":         "ReAcT,PLAN_EXECUTE",
			"includeChats": 1,
		}),
	}); err != nil {
		t.Fatalf("write mode-filtered agents websocket request: %v", err)
	}
	var frame ws.ResponseFrame
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("read mode-filtered agents websocket response: %v", err)
	}
	if frame.Code != 0 || frame.Type != "/api/agents" || frame.ID != "agents_mode_ws" {
		t.Fatalf("unexpected mode-filtered agents websocket frame: %#v", frame)
	}
	items, err := marshalResponseData[[]api.AgentSummary](frame.Data)
	if err != nil {
		t.Fatalf("decode websocket agent summaries: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("unexpected websocket mode-filtered agents: %#v", items)
	}

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    "/api/agents",
		ID:      "deprecated_agents_mode_ws",
		Payload: ws.MarshalPayload(map[string]any{"agentMode": "REACT"}),
	}); err != nil {
		t.Fatalf("write deprecated agents websocket request: %v", err)
	}
	var deprecated ws.ErrorFrame
	if err := conn.ReadJSON(&deprecated); err != nil {
		t.Fatalf("read deprecated agents websocket response: %v", err)
	}
	if deprecated.ID != "deprecated_agents_mode_ws" || deprecated.Code != http.StatusBadRequest || !strings.Contains(deprecated.Msg, "use mode instead") {
		t.Fatalf("unexpected deprecated agents websocket response: %#v", deprecated)
	}
}
