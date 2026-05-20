package server

import (
	"bytes"
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

func TestCatalogTagParametersAreIgnored(t *testing.T) {
	fixture := newTestFixture(t)

	skills := getAPIData[[]api.SkillSummary](t, fixture.server, http.MethodGet, "/api/skills", nil)
	skillsWithTag := getAPIData[[]api.SkillSummary](t, fixture.server, http.MethodGet, "/api/skills?tag=does-not-filter", nil)
	if len(skills) != len(skillsWithTag) {
		t.Fatalf("expected skills tag parameter to be ignored: all=%d tagged=%d", len(skills), len(skillsWithTag))
	}

	tools := getAPIData[[]api.ToolSummary](t, fixture.server, http.MethodGet, "/api/tools", nil)
	toolsWithTag := getAPIData[[]api.ToolSummary](t, fixture.server, http.MethodGet, "/api/tools?tag=does-not-filter", nil)
	if len(tools) != len(toolsWithTag) {
		t.Fatalf("expected tools tag parameter to be ignored: all=%d tagged=%d", len(tools), len(toolsWithTag))
	}
	kindTools := getAPIData[[]api.ToolSummary](t, fixture.server, http.MethodGet, "/api/tools?kind=does-not-exist", nil)
	if len(kindTools) != 0 || len(kindTools) > len(tools) {
		t.Fatalf("expected kind filter to remain active: all=%d kind=%d", len(tools), len(kindTools))
	}
}

func TestRenamedHTTPAPIRoutes(t *testing.T) {
	fixture := newTestFixture(t)
	seedSearchableChat(t, fixture.chats, "chat-route-search")

	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodPost, path: "/api/chats/search", body: `{"query":"rollback"}`},
		{method: http.MethodGet, path: "/api/chat/export?chatId=chat-route-search"},
	} {
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s expected 200, got %d: %s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}

	archiveServer, active, _ := newArchiveHandlerTestServer(t, nil)
	seedArchiveHandlerChat(t, active, "chat-route-archive")
	rec := httptest.NewRecorder()
	archiveServer.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/chat/archive", bytes.NewBufferString(`{"chatIds":["chat-route-archive"]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("archive setup expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	archiveServer.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/archives/search", bytes.NewBufferString(`{"query":"archived"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/archives/search expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRemovedHTTPAPIRoutesReturnNotFound(t *testing.T) {
	fixture := newTestFixture(t)
	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodPost, path: "/api/search", body: `{"query":"rollback"}`},
		{method: http.MethodPost, path: "/api/archive/search", body: `{"query":"rollback"}`},
		{method: http.MethodGet, path: "/api/chat-export?chatId=chat-route-search"},
		{method: http.MethodPost, path: "/api/session-search", body: `{"chatId":"chat-route-search","query":"rollback"}`},
		{method: http.MethodGet, path: "/api/archive-resource?chatId=chat-route-search&file=report.md"},
	} {
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body)))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s expected 404, got %d: %s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
}

func TestWebSocketSearchRoutesRenamed(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 4
		},
	})
	if fixture.server.wsHandler == nil {
		t.Fatal("expected websocket handler")
	}
	server := httptest.NewServer(fixture.server)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()
	readConnectedPush(t, conn)

	for _, tc := range []struct {
		route string
		id    string
	}{
		{route: "/api/chats/search", id: "new_chats_search"},
		{route: "/api/archives/search", id: "new_archives_search"},
	} {
		if err := conn.WriteJSON(ws.RequestFrame{
			Frame:   ws.FrameRequest,
			Type:    tc.route,
			ID:      tc.id,
			Payload: ws.MarshalPayload(map[string]any{"query": "rollback"}),
		}); err != nil {
			t.Fatalf("write %s request: %v", tc.route, err)
		}
		var frame ws.ResponseFrame
		if err := conn.ReadJSON(&frame); err != nil {
			t.Fatalf("read %s frame: %v", tc.route, err)
		}
		if frame.ID != tc.id || strings.Contains(frame.Msg, "unknown type") {
			t.Fatalf("expected %s to be registered, got %#v", tc.route, frame)
		}
	}

	for _, tc := range []struct {
		route string
		id    string
	}{
		{route: "/api/search", id: "old_search"},
		{route: "/api/archive/search", id: "old_archive_search"},
	} {
		if err := conn.WriteJSON(ws.RequestFrame{
			Frame:   ws.FrameRequest,
			Type:    tc.route,
			ID:      tc.id,
			Payload: ws.MarshalPayload(map[string]any{"query": "rollback"}),
		}); err != nil {
			t.Fatalf("write %s request: %v", tc.route, err)
		}
		var frame ws.ResponseFrame
		if err := conn.ReadJSON(&frame); err != nil {
			t.Fatalf("read %s frame: %v", tc.route, err)
		}
		if frame.ID != tc.id || !strings.Contains(frame.Msg, "unknown type") {
			t.Fatalf("expected %s to be removed, got %#v", tc.route, frame)
		}
	}
}

func getAPIData[T any](t *testing.T, server *Server, method string, path string, body []byte) T {
	t.Helper()
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(method, path, bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s %s expected 200, got %d: %s", method, path, rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[T]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response for %s %s: %v", method, path, err)
	}
	return response.Data
}

func seedSearchableChat(t *testing.T, store chat.Store, chatID string) {
	t.Helper()
	if _, _, err := store.EnsureChat(chatID, "mock-agent", "", "rollback plan"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := store.AppendQueryLine(chatID, chat.QueryLine{
		ChatID:    chatID,
		RunID:     "run-" + chatID,
		UpdatedAt: 1000,
		Query:     map[string]any{"role": "user", "message": "rollback plan"},
		Type:      "query",
	}); err != nil {
		t.Fatalf("append query: %v", err)
	}
	if err := store.OnRunCompleted(chat.RunCompletion{
		ChatID:          chatID,
		RunID:           "run-" + chatID,
		AgentKey:        "mock-agent",
		AssistantText:   "rollback completed",
		InitialMessage:  "rollback plan",
		FinishReason:    "complete",
		StartedAtMillis: 1000,
		UpdatedAtMillis: 2000,
	}); err != nil {
		t.Fatalf("complete run: %v", err)
	}
}
