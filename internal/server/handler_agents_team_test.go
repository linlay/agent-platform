package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

func TestAgentsIncludeTeamHTTPAndWebSocket(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, _ *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 8
			cfg.WebSocket.PingInterval = 30000
		},
		setupRuntime: setupOrchestratedTeamRuntime(t),
	})
	store, ok := fixture.chats.(*chat.FileStore)
	if !ok {
		t.Fatalf("expected file chat store, got %T", fixture.chats)
	}
	seedAgentModeChat(t, store, "chat-mock-agent", "loyw3v28", "mock-agent", "", "REACT", 1_000)
	seedAgentModeChat(t, store, "chat-research-team", "loyw3v2a", "", "research", "TEAM", 2_000)

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents?scope=nav&includeChats=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("default agents status=%d body=%s", rec.Code, rec.Body.String())
	}
	var defaultResponse api.ApiResponse[[]api.AgentSummary]
	if err := json.Unmarshal(rec.Body.Bytes(), &defaultResponse); err != nil {
		t.Fatalf("decode default agents response: %v", err)
	}
	if len(defaultResponse.Data) != 1 || defaultResponse.Data[0].Key != "mock-agent" || strings.Contains(rec.Body.String(), `"kind"`) {
		t.Fatalf("default agents response must remain agent-only and unchanged: %s", rec.Body.String())
	}
	expectedAgentConfigDir := filepath.Join(fixture.cfg.Paths.AgentsDir, "mock-agent")
	if defaultResponse.Data[0].AgentConfigDir != expectedAgentConfigDir {
		t.Fatalf("default agent config dir = %q, want %q", defaultResponse.Data[0].AgentConfigDir, expectedAgentConfigDir)
	}

	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents?scope=nav&includeTeam=true&includeChats=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("includeTeam agents status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[[]api.AgentCatalogSummary]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode includeTeam response: %v", err)
	}
	if len(response.Data) != 3 {
		t.Fatalf("expected mock agent and two teams, got %#v", response.Data)
	}
	if response.Data[0].Kind != "team" || response.Data[0].TeamID != "research" || response.Data[1].Kind != "agent" || response.Data[1].Key != "mock-agent" || response.Data[2].Kind != "team" || response.Data[2].TeamID != "default" {
		t.Fatalf("catalog should mix by latest run id and put no-chat teams last: %#v", response.Data)
	}
	research := response.Data[0]
	if research.Key != "" || research.Mode != "" || research.AgentConfigDir != "" || len(research.AgentKeys) != 2 || research.Stats.TotalCount != 1 || research.Stats.UnreadCount != 1 {
		t.Fatalf("unexpected Team summary: %#v", research)
	}
	if len(research.Chats) != 1 || research.Chats[0].ChatID != "chat-research-team" || research.Chats[0].Usage != nil {
		t.Fatalf("team chats should match agent includeChats behavior: %#v", research.Chats)
	}
	if response.Data[1].AgentConfigDir != expectedAgentConfigDir || response.Data[1].Stats.TotalCount != 1 || response.Data[1].Stats.UnreadCount != 1 || len(response.Data[1].Chats) != 1 {
		t.Fatalf("agent summary should retain stats and chats: %#v", response.Data[1])
	}
	researchJSON, err := json.Marshal(research)
	if err != nil {
		t.Fatalf("marshal Team summary: %v", err)
	}
	if strings.Contains(string(researchJSON), `"agentConfigDir"`) {
		t.Fatalf("team items must omit agentConfigDir: %s", researchJSON)
	}
	if strings.Contains(rec.Body.String(), `"key":""`) || strings.Contains(rec.Body.String(), `"mode":""`) {
		t.Fatalf("team items must omit Agent-only empty fields: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"runtimeMode"`) {
		t.Fatalf("team items must not expose retired runtime metadata: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents?includeTeam=true&mode=TEAM", nil))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "mode TEAM is internal") {
		t.Fatalf("TEAM mode must be rejected at the public catalog boundary: status=%d body=%s", rec.Code, rec.Body.String())
	}

	for _, raw := range []string{"maybe", ""} {
		rec = httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents?includeTeam="+raw, nil))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "includeTeam must be a boolean") {
			t.Fatalf("invalid includeTeam=%q should be rejected, status=%d body=%s", raw, rec.Code, rec.Body.String())
		}
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
		ID:    "agents_include_team",
		Payload: ws.MarshalPayload(map[string]any{
			"scope":        "nav",
			"includeTeam":  true,
			"includeChats": 1,
		}),
	}); err != nil {
		t.Fatalf("write includeTeam websocket request: %v", err)
	}
	var frame ws.ResponseFrame
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("read includeTeam websocket response: %v", err)
	}
	items, err := marshalResponseData[[]api.AgentCatalogSummary](frame.Data)
	if err != nil {
		t.Fatalf("decode includeTeam websocket response: %v", err)
	}
	if frame.Code != 0 || len(items) != 3 || items[0].Kind != "team" || items[0].TeamID != "research" || items[0].AgentConfigDir != "" || items[1].Kind != "agent" || items[1].AgentConfigDir != expectedAgentConfigDir {
		t.Fatalf("websocket includeTeam should match HTTP: %#v", frame)
	}

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    "/api/agents",
		ID:      "agents_include_team_invalid",
		Payload: ws.MarshalPayload(map[string]any{"includeTeam": "true"}),
	}); err != nil {
		t.Fatalf("write invalid includeTeam websocket request: %v", err)
	}
	var invalid ws.ErrorFrame
	if err := conn.ReadJSON(&invalid); err != nil {
		t.Fatalf("read invalid includeTeam websocket response: %v", err)
	}
	if invalid.ID != "agents_include_team_invalid" || invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid includeTeam websocket payload should fail: %#v", invalid)
	}
}
