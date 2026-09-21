package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/ws"
	gws "github.com/gorilla/websocket"
)

func TestChatOrderSnapshotHTTPAndWS(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) { writeProviderSSE(t, w, `[DONE]`) }, testFixtureOptions{notifications: ws.NewHub()})
	store := fixture.chats.(*chat.FileStore)
	read := func() api.ChatOrderSnapshotResponse {
		t.Helper()
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chats/order", nil))
		if rec.Code != 200 {
			t.Fatalf("read order: %d %s", rec.Code, rec.Body.String())
		}
		var response api.ApiResponse[api.ChatOrderSnapshotResponse]
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Data.PinnedChats == nil {
			t.Fatal("pinnedChats must be an array, including when empty")
		}
		return response.Data
	}
	if snapshot := read(); len(snapshot.PinnedChats) != 0 || snapshot.SortMode != "recent" {
		t.Fatalf("empty snapshot: %+v", snapshot)
	}
	for i := 0; i < 30; i++ {
		id := fmt.Sprintf("pin-%02d", i)
		agentKey, teamID, mode := "mock-agent", "", "REACT"
		if i%3 == 1 {
			agentKey, mode = "coder", "CODER"
		}
		if i%3 == 2 {
			agentKey, teamID, mode = "", "team", "TEAM"
		}
		seedAgentModeChat(t, store, id, fmt.Sprintf("loyw3v%02d", i), agentKey, teamID, mode, int64(1000+i))
		if _, _, err := store.SetChatPinned(id, true); err != nil {
			t.Fatal(err)
		}
	}
	seedAgentModeChat(t, store, "ordinary", "loyw3w00", "mock-agent", "", "REACT", 9000)
	_, control, _ := fixture.runs.Register(context.Background(), contracts.QuerySession{
		RunID: "active-pin", ChatID: "pin-00", AgentKey: "mock-agent", RunOwner: contracts.AgentRunOwner("mock-agent", ""), StartedAtMillis: testEpochMillis + 10000,
	})
	control.TransitionState(contracts.RunLoopStateWaitingSubmit)
	snapshot := read()
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chats?pinned=true", nil))
	var list api.ApiResponse[[]api.ChatSummaryResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.PinnedChats) != 30 || !reflect.DeepEqual(snapshot.PinnedChats, list.Data) {
		t.Fatalf("snapshot lost summary fields or order: %+v", snapshot)
	}
	if !reflect.DeepEqual(snapshot.PinnedOrder, apiChatIDs(snapshot.PinnedChats)) {
		t.Fatal("legacy IDs diverged from snapshot")
	}
	if snapshot.PinnedChats[0].TeamID != "team" {
		t.Fatal("team owner missing")
	}
	assertSummaryActiveRun(t, chatSummaryByID(t, snapshot.PinnedChats, "pin-00"), "active-pin", testEpochMillis+10000)

	server := httptest.NewServer(fixture.server)
	defer server.Close()
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	readConnectedPush(t, conn)
	writeChatOrderWSRequest(t, conn, "snapshot", nil)
	var frame ws.ResponseFrame
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatal(err)
	}
	if frame.Frame != ws.FrameResponse || frame.ID != "snapshot" || frame.Code != 0 {
		t.Fatalf("unexpected frame: %+v", frame)
	}
	wsSnapshot, err := marshalResponseData[api.ChatOrderSnapshotResponse](frame.Data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(wsSnapshot, snapshot) {
		t.Fatal("HTTP and WS snapshots differ")
	}

	// Mutations acknowledge persistence without loading runtime summaries.
	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/chats/order", strings.NewReader(`{"operation":"set_pinned","chatId":"pin-00","pinned":false}`)))
	var ack api.ApiResponse[map[string]json.RawMessage]
	if rec.Code != 200 {
		t.Fatalf("mutation: %s", rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ack); err != nil {
		t.Fatal(err)
	}
	if _, exists := ack.Data["pinnedChats"]; exists {
		t.Fatal("mutation must remain lightweight")
	}
	if len(read().PinnedChats) != 29 {
		t.Fatal("unpin was not reflected in refreshed snapshot")
	}
}
