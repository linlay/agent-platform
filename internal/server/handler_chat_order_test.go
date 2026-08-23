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

func TestChatOrderHTTPAndWebSocketShareCanonicalState(t *testing.T) {
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
	seedAgentModeChat(t, store, "chat-old", "loyw3v28", "agent-react", "", "REACT", 1_000)
	seedAgentModeChat(t, store, "chat-middle", "loyw3v29", "agent-react", "", "REACT", 2_000)
	seedAgentModeChat(t, store, "chat-new", "loyw3v2a", "agent-react", "", "REACT", 3_000)

	order := readChatOrderHTTP(t, fixture.server)
	if order.SortMode != "recent" || order.UpdatedAt != nil {
		t.Fatalf("default order = %#v", order)
	}
	order = updateChatOrderHTTP(t, fixture.server, api.UpdateChatOrderRequest{
		Operation:    "move",
		ChatID:       "chat-old",
		BeforeChatID: "chat-new",
	}, http.StatusOK)
	if order.SortMode != "manual" || order.UpdatedAt == nil {
		t.Fatalf("moved order = %#v", order)
	}
	assertChatsLimitHTTP(t, fixture.server, "/api/chats?mode=REACT&limit=2", []string{"chat-old", "chat-new"})

	updateChatOrderHTTP(t, fixture.server, api.UpdateChatOrderRequest{
		Operation:    "move",
		ChatID:       "chat-old",
		BeforeChatID: "chat-new",
		AfterChatID:  "chat-middle",
	}, http.StatusBadRequest)

	httpServer := httptest.NewServer(fixture.server)
	defer httpServer.Close()
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()
	readConnectedPush(t, conn)

	writeChatOrderWSRequest(t, conn, "order_get", nil)
	assertChatOrderWSResponse(t, conn, "order_get", "manual")
	writeChatsLimitWSRequest(t, conn, "manual_chats", map[string]any{"mode": "REACT"})
	assertChatsLimitWSResponse(t, conn, "manual_chats", []string{"chat-old", "chat-new", "chat-middle"})

	writeChatOrderWSRequest(t, conn, "order_recent", map[string]any{
		"operation": "set_mode",
		"sortMode":  "recent",
	})
	assertChatOrderWSResponse(t, conn, "order_recent", "recent")
	writeChatsLimitWSRequest(t, conn, "recent_chats", map[string]any{"mode": "REACT"})
	assertChatsLimitWSResponse(t, conn, "recent_chats", []string{"chat-new", "chat-middle", "chat-old"})

	writeChatOrderWSRequest(t, conn, "order_manual", map[string]any{
		"operation": "set_mode",
		"sortMode":  "manual",
	})
	assertChatOrderWSResponse(t, conn, "order_manual", "manual")
	writeChatsLimitWSRequest(t, conn, "manual_restored", map[string]any{"mode": "REACT"})
	assertChatsLimitWSResponse(t, conn, "manual_restored", []string{"chat-old", "chat-new", "chat-middle"})
}

func readChatOrderHTTP(t *testing.T, handler http.Handler) api.ChatOrderResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/chats/order", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/chats/order status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response api.ApiResponse[api.ChatOrderResponse]
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode GET /api/chats/order: %v", err)
	}
	return response.Data
}

func updateChatOrderHTTP(t *testing.T, handler http.Handler, request api.UpdateChatOrderRequest, wantStatus int) api.ChatOrderResponse {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, "/api/chats/order", strings.NewReader(string(body))))
	if recorder.Code != wantStatus {
		t.Fatalf("PUT /api/chats/order status=%d want=%d body=%s", recorder.Code, wantStatus, recorder.Body.String())
	}
	if wantStatus != http.StatusOK {
		return api.ChatOrderResponse{}
	}
	var response api.ApiResponse[api.ChatOrderResponse]
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode PUT /api/chats/order: %v", err)
	}
	return response.Data
}

func writeChatOrderWSRequest(t *testing.T, conn *gws.Conn, id string, payload map[string]any) {
	t.Helper()
	if err := conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    "/api/chats/order",
		ID:      id,
		Payload: ws.MarshalPayload(payload),
	}); err != nil {
		t.Fatalf("write websocket request %s: %v", id, err)
	}
}

func assertChatOrderWSResponse(t *testing.T, conn *gws.Conn, id string, wantMode string) {
	t.Helper()
	var frame ws.ResponseFrame
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("read websocket response %s: %v", id, err)
	}
	if frame.Frame != ws.FrameResponse || frame.Type != "/api/chats/order" || frame.ID != id || frame.Code != 0 {
		t.Fatalf("unexpected websocket response %s: %#v", id, frame)
	}
	response, err := marshalResponseData[api.ChatOrderResponse](frame.Data)
	if err != nil {
		t.Fatalf("decode websocket response %s: %v", id, err)
	}
	if response.SortMode != wantMode {
		t.Fatalf("websocket sort mode=%q want=%q", response.SortMode, wantMode)
	}
}
