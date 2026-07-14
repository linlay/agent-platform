package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

func TestChatsLimitHTTPAndWebSocket(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 8
			cfg.WebSocket.PingInterval = 30000
		},
	})
	store, ok := fixture.chats.(*chat.FileStore)
	if !ok {
		t.Fatalf("expected file chat store, got %T", fixture.chats)
	}
	seedAgentModeChat(t, store, "chat-react-limit", "loyw3v28", "agent-react", "", "REACT", 1_000)
	seedAgentModeChat(t, store, "chat-plan-limit", "loyw3v29", "agent-plan", "", "PLAN_EXECUTE", 2_000)
	seedAgentModeChat(t, store, "chat-team-limit", "loyw3v2a", "", "team-a", "REACT", 3_000)

	assertChatsLimitHTTP(t, fixture.server, "/api/chats", []string{"chat-team-limit", "chat-plan-limit", "chat-react-limit"})
	assertChatsLimitHTTP(t, fixture.server, "/api/chats?limit=2", []string{"chat-team-limit", "chat-plan-limit"})
	assertChatsLimitHTTP(t, fixture.server, "/api/chats?mode=REACT&lastRunId=loyw3v27&limit=1", []string{"chat-team-limit"})
	assertChatsLimitHTTP(t, fixture.server, "/api/chats?agentKey=agent-react&mode=REACT&lastRunId=loyw3v27&limit=1", []string{"chat-react-limit"})

	for _, rawLimit := range []string{"", "0", "-1", "abc"} {
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chats?limit="+rawLimit, nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("limit=%q status=%d want 400 body=%s", rawLimit, rec.Code, rec.Body.String())
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

	writeChatsLimitWSRequest(t, conn, "chats_default_limit", map[string]any{"mode": "REACT"})
	assertChatsLimitWSResponse(t, conn, "chats_default_limit", []string{"chat-team-limit", "chat-react-limit"})
	writeChatsLimitWSRequest(t, conn, "chats_limited", map[string]any{"mode": "REACT", "limit": 1})
	assertChatsLimitWSResponse(t, conn, "chats_limited", []string{"chat-team-limit"})
	writeChatsLimitWSRequest(t, conn, "chats_invalid_limit", map[string]any{"limit": 0})
	var invalid ws.ErrorFrame
	if err := conn.ReadJSON(&invalid); err != nil {
		t.Fatalf("read invalid-limit websocket response: %v", err)
	}
	if invalid.Frame != ws.FrameError || invalid.ID != "chats_invalid_limit" || invalid.Code != http.StatusBadRequest {
		t.Fatalf("unexpected invalid-limit websocket response: %#v", invalid)
	}
}

func assertChatsLimitHTTP(t *testing.T, handler http.Handler, path string, wantIDs []string) {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[[]api.ChatSummaryResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode %s response: %v", path, err)
	}
	if got := apiChatIDs(response.Data); strings.Join(got, ",") != strings.Join(wantIDs, ",") {
		t.Fatalf("%s chats=%v want=%v", path, got, wantIDs)
	}
}

func writeChatsLimitWSRequest(t *testing.T, conn *gws.Conn, id string, payload map[string]any) {
	t.Helper()
	if err := conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    "/api/chats",
		ID:      id,
		Payload: ws.MarshalPayload(payload),
	}); err != nil {
		t.Fatalf("write websocket request %s: %v", id, err)
	}
}

func assertChatsLimitWSResponse(t *testing.T, conn *gws.Conn, id string, wantIDs []string) {
	t.Helper()
	var frame ws.ResponseFrame
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("read websocket response %s: %v", id, err)
	}
	if frame.Frame != ws.FrameResponse || frame.Type != "/api/chats" || frame.ID != id || frame.Code != 0 {
		t.Fatalf("unexpected websocket response %s: %#v", id, frame)
	}
	items, err := marshalResponseData[[]api.ChatSummaryResponse](frame.Data)
	if err != nil {
		t.Fatalf("decode websocket response %s: %v", id, err)
	}
	if got := apiChatIDs(items); strings.Join(got, ",") != strings.Join(wantIDs, ",") {
		t.Fatalf("websocket chats=%v want=%v", got, wantIDs)
	}
}
