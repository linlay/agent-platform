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

func TestChatsModeFiltersHTTPAndWebSocket(t *testing.T) {
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
	seedAgentModeChat(t, store, "chat-react", "loyw3v28", "agent-react", "", "REACT", 1_000)
	seedAgentModeChat(t, store, "chat-plan", "loyw3v29", "agent-plan", "", "PLAN_EXECUTE", 2_000)
	seedAgentModeChat(t, store, "chat-team", "loyw3v2a", "", "team-a", "REACT", 3_000)
	if _, _, err := store.EnsureChat("chat-history", "agent-history", "", "legacy"); err != nil {
		t.Fatalf("ensure historical chat: %v", err)
	}

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chats?mode=react,TEAM&mode=PLAN_EXECUTE", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("mode-filtered HTTP status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[[]api.ChatSummaryResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode mode-filtered HTTP response: %v", err)
	}
	if got := apiChatIDs(response.Data); strings.Join(got, ",") != "chat-team,chat-plan,chat-react" {
		t.Fatalf("unexpected HTTP order: %v", got)
	}
	if response.Data[0].Mode != "TEAM" || response.Data[1].Mode != "PLAN-EXECUTE" || response.Data[2].Mode != "REACT" {
		t.Fatalf("expected normalized summary modes, got %#v", response.Data)
	}
	if strings.Contains(rec.Body.String(), `"agentMode"`) {
		t.Fatalf("chat summaries must not expose agentMode: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chats?mode=REACT,TEAM&agentKey=agent-react&lastRunId=loyw3v27", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode combined-filter HTTP response: %v", err)
	}
	if len(response.Data) != 1 || response.Data[0].ChatID != "chat-react" {
		t.Fatalf("agentKey and lastRunId must combine with mode using AND, got %#v", response.Data)
	}

	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chats?mode=unknown", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode unknown-mode response: %v", err)
	}
	if len(response.Data) != 1 || response.Data[0].ChatID != "chat-team" {
		t.Fatalf("team-owned chats should bypass unknown mode, got %#v", response.Data)
	}

	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chats?mode=REACT", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode react-only response: %v", err)
	}
	if got := apiChatIDs(response.Data); strings.Join(got, ",") != "chat-team,chat-react" {
		t.Fatalf("team-owned chats should remain alongside matching agents, got %#v", response.Data)
	}
	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chats?agentMode=REACT", nil))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "use mode instead") {
		t.Fatalf("deprecated agentMode should be rejected, status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chat?chatId=chat-plan", nil))
	var detail api.ApiResponse[api.ChatDetailResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode chat detail: %v", err)
	}
	if len(detail.Data.Runs) != 1 || detail.Data.Runs[0].Mode != "PLAN-EXECUTE" {
		t.Fatalf("chat detail should expose run mode, got %#v", detail.Data.Runs)
	}
	if strings.Contains(rec.Body.String(), `"agentMode"`) {
		t.Fatalf("chat detail must not expose agentMode: %s", rec.Body.String())
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
		Type:  "/api/chats",
		ID:    "mode_ws",
		Payload: ws.MarshalPayload(map[string]any{
			"mode": "team,ReAcT",
		}),
	}); err != nil {
		t.Fatalf("write mode-filtered websocket request: %v", err)
	}
	var frame ws.ResponseFrame
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("read mode-filtered websocket response: %v", err)
	}
	if frame.Code != 0 || frame.Type != "/api/chats" || frame.ID != "mode_ws" {
		t.Fatalf("unexpected mode-filtered websocket frame: %#v", frame)
	}
	data, err := marshalResponseData[[]api.ChatSummaryResponse](frame.Data)
	if err != nil {
		t.Fatalf("decode websocket summaries: %v", err)
	}
	if got := apiChatIDs(data); strings.Join(got, ",") != "chat-team,chat-react" || data[0].Mode != "TEAM" || data[1].Mode != "REACT" {
		t.Fatalf("unexpected websocket mode filter result: %#v", data)
	}
	if err := conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    "/api/chats",
		ID:      "mode_ws_unknown",
		Payload: ws.MarshalPayload(map[string]any{"mode": "unknown"}),
	}); err != nil {
		t.Fatalf("write unknown-mode websocket request: %v", err)
	}
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("read unknown-mode websocket response: %v", err)
	}
	data, err = marshalResponseData[[]api.ChatSummaryResponse](frame.Data)
	if err != nil {
		t.Fatalf("decode unknown-mode websocket summaries: %v", err)
	}
	if len(data) != 1 || data[0].ChatID != "chat-team" {
		t.Fatalf("team-owned websocket chats should bypass mode, got %#v", data)
	}
	if err := conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    "/api/chats",
		ID:      "deprecated_mode_ws",
		Payload: ws.MarshalPayload(map[string]any{"agentMode": "REACT"}),
	}); err != nil {
		t.Fatalf("write deprecated websocket request: %v", err)
	}
	var deprecated ws.ErrorFrame
	if err := conn.ReadJSON(&deprecated); err != nil {
		t.Fatalf("read deprecated websocket response: %v", err)
	}
	if deprecated.ID != "deprecated_mode_ws" || deprecated.Code != http.StatusBadRequest || !strings.Contains(deprecated.Msg, "use mode instead") {
		t.Fatalf("unexpected deprecated websocket response: %#v", deprecated)
	}
}

func seedAgentModeChat(t *testing.T, store *chat.FileStore, chatID string, runID string, agentKey string, teamID string, agentMode string, offset int64) {
	t.Helper()
	if _, _, err := store.EnsureChatWithSourceAndMode(chatID, agentKey, teamID, "question", "", agentMode); err != nil {
		t.Fatalf("ensure %s: %v", chatID, err)
	}
	startedAt := int64(1_700_000_000_000 + offset)
	if err := store.OnRunStarted(chat.RunStart{
		ChatID:          chatID,
		RunID:           runID,
		AgentKey:        agentKey,
		AgentMode:       agentMode,
		TeamID:          teamID,
		InitialMessage:  "question",
		StartedAtMillis: startedAt,
	}); err != nil {
		t.Fatalf("start %s: %v", chatID, err)
	}
	if err := store.OnRunCompleted(chat.RunCompletion{
		ChatID:          chatID,
		RunID:           runID,
		AgentKey:        agentKey,
		AgentMode:       agentMode,
		TeamID:          teamID,
		InitialMessage:  "question",
		AssistantText:   "answer",
		FinishReason:    "complete",
		StartedAtMillis: startedAt,
		UpdatedAtMillis: startedAt + 1,
	}); err != nil {
		t.Fatalf("complete %s: %v", chatID, err)
	}
}

func apiChatIDs(items []api.ChatSummaryResponse) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ChatID)
	}
	return ids
}
