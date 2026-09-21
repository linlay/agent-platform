package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/platformcontrol"
	"agent-platform/internal/ws"
	gws "github.com/gorilla/websocket"
)

func TestChatPinToolSharesHTTPStateAndWebSocketNotifications(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{notifications: ws.NewHub()})
	seedAgentModeChat(t, fixture.chats.(*chat.FileStore), "tool-pin", "loyw3v00", "mock-agent", "", "REACT", 1000)
	httpServer := httptest.NewServer(fixture.server)
	defer httpServer.Close()
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	readConnectedPush(t, conn)
	handler := platformcontrol.NewToolHandler(config.Config{PlatformControl: config.PlatformControlConfig{Enabled: true}}, nil, fixture.server.conversationService())
	caller := &contracts.ExecutionContext{Session: contracts.QuerySession{
		RunID: "run-pin", ChatID: "tool-pin", AgentKey: "mock-agent", Mode: "REACT",
		RunOwner: contracts.AgentRunOwner("mock-agent", ""), ToolNames: []string{"platform_control"},
	}}
	invoke := func() {
		t.Helper()
		result, err := handler.Invoke(t.Context(), "platform_control", map[string]any{
			"operation": "chat.set_pinned", "params": map[string]any{"pinned": true},
		}, caller)
		if err != nil || result.Error != "" {
			t.Fatalf("pin tool: %+v %v", result, err)
		}
	}
	invoke()
	var push ws.PushFrame
	if err := conn.ReadJSON(&push); err != nil {
		t.Fatal(err)
	}
	if push.Type != "chats.order.changed" {
		t.Fatalf("tool push: %+v", push)
	}
	assertChatsLimitHTTP(t, fixture.server, "/api/chats?pinned=true", []string{"tool-pin"})
	invoke() // No-op must not enqueue another push before the response below.
	yes, no := true, false
	updateChatOrderHTTP(t, fixture.server, api.UpdateChatOrderRequest{Operation: "set_pinned", ChatID: "tool-pin", Pinned: &yes}, 200)
	writeChatOrderWSRequest(t, conn, "noop-check", map[string]any{})
	assertChatOrderWSResponse(t, conn, "noop-check", "recent")
	updateChatOrderHTTP(t, fixture.server, api.UpdateChatOrderRequest{Operation: "set_pinned", ChatID: "tool-pin", Pinned: &no}, 200)
	if err := conn.ReadJSON(&push); err != nil {
		t.Fatal(err)
	}
	if push.Type != "chats.order.changed" {
		t.Fatalf("HTTP push: %+v", push)
	}
	writeChatOrderWSRequest(t, conn, "single-push-check", map[string]any{})
	assertChatOrderWSResponse(t, conn, "single-push-check", "recent")
}

func TestChatPinnedHTTPAndWSFilterBeforeLimits(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) { writeProviderSSE(t, w, `[DONE]`) }, testFixtureOptions{notifications: ws.NewHub()})
	store := fixture.chats.(*chat.FileStore)
	for i := 0; i < 12; i++ {
		seedAgentModeChat(t, store, fmt.Sprintf("chat-%02d", i), fmt.Sprintf("loyw3v%02d", i), "mock-agent", "", "REACT", int64(1000+i))
	}
	seedAgentModeChat(t, store, "chat-kbase", "loyw3w00", "kbase", "", "KBASE", 2000)
	seedAgentModeChat(t, store, "chat-coder", "loyw3w01", "coder", "", "CODER", 3000)
	seedAgentModeChat(t, store, "chat-team", "loyw3w02", "", "team-pins", "TEAM", 4000)
	yes, no := true, false
	for _, id := range []string{"chat-11", "chat-10", "chat-09", "chat-kbase", "chat-coder", "chat-team"} {
		updateChatOrderHTTP(t, fixture.server, api.UpdateChatOrderRequest{Operation: "set_pinned", ChatID: id, Pinned: &yes}, 200)
	}
	assertChatsLimitHTTP(t, fixture.server, "/api/chats?pinned=false&mode=REACT&limit=8", []string{"chat-08", "chat-07", "chat-06", "chat-05", "chat-04", "chat-03", "chat-02", "chat-01"})
	assertChatsLimitHTTP(t, fixture.server, "/api/chats?pinned=true", []string{"chat-team", "chat-coder", "chat-kbase", "chat-09", "chat-10", "chat-11"})
	assertChatsLimitHTTP(t, fixture.server, "/api/chats?mode=KBASE&pinned=true", []string{"chat-team", "chat-kbase"})
	assertChatsLimitHTTP(t, fixture.server, "/api/chats?limit=2", []string{"chat-team", "chat-coder"})
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest("GET", "/api/agents?includeChats=8&chatsPinned=false", nil))
	var agents api.ApiResponse[[]api.AgentSummary]
	if err := json.Unmarshal(rec.Body.Bytes(), &agents); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, agent := range agents.Data {
		if agent.Key == "mock-agent" {
			found = true
			if len(agent.Chats) != 8 || agent.Chats[0].ChatID != "chat-08" {
				t.Fatalf("owner limit: %+v", agent.Chats)
			}
			for _, sum := range agent.Chats {
				if sum.Pinned {
					t.Fatal("pinned in owner preview")
				}
			}
		}
	}
	if !found {
		t.Fatalf("mock agent missing: %s", rec.Body)
	}
	detail, err := fixture.server.loadChatDetail(t.Context(), "chat-coder", false)
	if err != nil || !detail.Pinned {
		t.Fatalf("detail pin=%v err=%v", detail.Pinned, err)
	}
	for _, url := range []string{"/api/chats?pinned=", "/api/chats?pinned=0", "/api/chats?pinned=true&pinned=false", "/api/agents?chatsPinned=null"} {
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
		if rec.Code != 400 {
			t.Fatalf("%s: %d", url, rec.Code)
		}
	}
	updateChatOrderHTTP(t, fixture.server, api.UpdateChatOrderRequest{Operation: "set_pinned", ChatID: "chat-11"}, 400)
	updateChatOrderHTTP(t, fixture.server, api.UpdateChatOrderRequest{Operation: "set_pinned", ChatID: "missing", Pinned: &yes}, 404)
	updateChatOrderHTTP(t, fixture.server, api.UpdateChatOrderRequest{Operation: "set_pinned", ChatID: "missing", Pinned: &no}, 200)
	httpServer := httptest.NewServer(fixture.server)
	defer httpServer.Close()
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	readConnectedPush(t, conn)
	writeChatOrderWSRequest(t, conn, "unpin", map[string]any{"operation": "set_pinned", "chatId": "chat-11", "pinned": false})
	var push ws.PushFrame
	if err := conn.ReadJSON(&push); err != nil {
		t.Fatal(err)
	}
	if push.Type != "chats.order.changed" {
		t.Fatalf("pin push: %+v", push)
	}
	assertChatOrderWSResponse(t, conn, "unpin", "recent")
	writeChatsLimitWSRequest(t, conn, "unpinned", map[string]any{"pinned": false, "mode": "REACT", "limit": 8})
	assertChatsLimitWSResponse(t, conn, "unpinned", []string{"chat-11", "chat-08", "chat-07", "chat-06", "chat-05", "chat-04", "chat-03", "chat-02"})
	writeChatsLimitWSRequest(t, conn, "pins", map[string]any{"pinned": true})
	assertChatsLimitWSResponse(t, conn, "pins", []string{"chat-team", "chat-coder", "chat-kbase", "chat-09", "chat-10"})
	if err := conn.WriteJSON(ws.RequestFrame{Frame: ws.FrameRequest, Type: "/api/agents", ID: "agents", Payload: marshalPayload(map[string]any{"includeChats": 8, "chatsPinned": false})}); err != nil {
		t.Fatal(err)
	}
	var frame ws.ResponseFrame
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatal(err)
	}
	wsAgents, err := marshalResponseData[[]api.AgentSummary](frame.Data)
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range wsAgents {
		if agent.Key == "mock-agent" && !reflect.DeepEqual(apiChatIDs(agent.Chats), []string{"chat-11", "chat-08", "chat-07", "chat-06", "chat-05", "chat-04", "chat-03", "chat-02"}) {
			t.Fatalf("WS owner preview: %+v", agent.Chats)
		}
	}
}
