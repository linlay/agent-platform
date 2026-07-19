package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
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

func TestChatsActiveRunHTTPAndWebSocket(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 8
			cfg.WebSocket.PingInterval = 30000
		},
	})
	const (
		chatID         = "chat-active-summary"
		persistedRunID = "run-persisted-summary"
		activeRunID    = "run-active-summary"
	)
	persistedStartedAt := testEpochMillis + 1_000
	activeStartedAt := testEpochMillis + 2_000
	if _, _, err := fixture.chats.EnsureChat(chatID, "mock-agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := completeServerFixtureRun(t, fixture.chats, chat.RunCompletion{
		ChatID:          chatID,
		RunID:           persistedRunID,
		AgentKey:        "mock-agent",
		InitialMessage:  "hello",
		AssistantText:   "done",
		StartedAtMillis: persistedStartedAt,
		UpdatedAtMillis: persistedStartedAt + 1,
		Usage:           chat.UsageData{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10},
	}); err != nil {
		t.Fatalf("complete persisted run: %v", err)
	}
	runs := fixture.runs.(*contracts.InMemoryRunManager)
	_, control, _ := runs.Register(context.Background(), contracts.QuerySession{
		RunID:           activeRunID,
		ChatID:          chatID,
		AgentKey:        "mock-agent",
		RunOwner:        contracts.AgentRunOwner("mock-agent", ""),
		StartedAtMillis: activeStartedAt,
	})
	control.TransitionState(contracts.RunLoopStateWaitingSubmit)

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP /api/chats status=%d body=%s", rec.Code, rec.Body.String())
	}
	var httpResponse api.ApiResponse[[]api.ChatSummaryResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &httpResponse); err != nil {
		t.Fatalf("decode HTTP /api/chats response: %v", err)
	}
	httpSummary := chatSummaryByID(t, httpResponse.Data, chatID)
	if httpSummary.Usage == nil || httpSummary.Usage.TotalTokens != 10 {
		t.Fatalf("HTTP /api/chats should retain usage, got %#v", httpSummary.Usage)
	}
	assertSummaryActiveRun(t, httpSummary, activeRunID, activeStartedAt)

	httpServer := httptest.NewServer(fixture.server)
	defer httpServer.Close()
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()
	readConnectedPush(t, conn)
	writeChatsLimitWSRequest(t, conn, "chats_active_run", nil)
	var frame ws.ResponseFrame
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("read websocket active-run response: %v", err)
	}
	if frame.Frame != ws.FrameResponse || frame.Type != "/api/chats" || frame.ID != "chats_active_run" || frame.Code != 0 {
		t.Fatalf("unexpected websocket active-run response: %#v", frame)
	}
	wsSummaries, err := marshalResponseData[[]api.ChatSummaryResponse](frame.Data)
	if err != nil {
		t.Fatalf("decode websocket active-run summaries: %v", err)
	}
	wsSummary := chatSummaryByID(t, wsSummaries, chatID)
	assertSummaryActiveRun(t, wsSummary, activeRunID, activeStartedAt)
	if wsSummary.Usage == nil || wsSummary.Usage.TotalTokens != 10 {
		t.Fatalf("WebSocket /api/chats should retain usage, got %#v", wsSummary.Usage)
	}
	if *wsSummary.ActiveRun != *httpSummary.ActiveRun {
		t.Fatalf("HTTP and WebSocket activeRun differ: http=%#v ws=%#v", httpSummary.ActiveRun, wsSummary.ActiveRun)
	}
}

func chatSummaryByID(t *testing.T, summaries []api.ChatSummaryResponse, chatID string) api.ChatSummaryResponse {
	t.Helper()
	for _, summary := range summaries {
		if summary.ChatID == chatID {
			return summary
		}
	}
	t.Fatalf("chat %q not found in summaries %#v", chatID, summaries)
	return api.ChatSummaryResponse{}
}

func assertSummaryActiveRun(t *testing.T, summary api.ChatSummaryResponse, runID string, startedAt int64) {
	t.Helper()
	if summary.ActiveRun == nil ||
		summary.ActiveRun.RunID != runID ||
		summary.ActiveRun.State != string(contracts.RunLoopStateWaitingSubmit) ||
		summary.ActiveRun.StartedAt != startedAt {
		t.Fatalf("unexpected activeRun %#v", summary.ActiveRun)
	}
	if summary.ActiveRun.PlanningMode {
		t.Fatalf("summary activeRun should not include planningMode, got %#v", summary.ActiveRun)
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
