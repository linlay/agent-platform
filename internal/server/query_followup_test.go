package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/i18n"
	"agent-platform/internal/querymessages"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

func TestReferenceOnlyQueryRequiresMainHistory(t *testing.T) {
	fixture := newTestFixture(t)
	const chatID = "query-followup"
	selection := []api.Reference{{Type: "selection", Text: "selected passage"}}
	admit := func(refs []api.Reference) error {
		prepared, err := fixture.server.prepareQueryAdmissionRequest(t.Context(), api.QueryRequest{ChatID: chatID, AgentKey: "mock-agent", References: refs}, true, i18n.DefaultLocale, "http://example.com")
		releaseQuery(prepared.Release)
		return err
	}
	for _, existing := range []bool{false, true} {
		if existing {
			if _, _, err := fixture.chats.EnsureChat(chatID, "mock-agent", ""); err != nil {
				t.Fatal(err)
			}
		}
		if err := admit(nil); err == nil {
			t.Fatal("empty first query accepted")
		}
		err := admit(selection)
		var statusErr *statusError
		if !errors.As(err, &statusErr) || statusErr.Status != 400 || !strings.Contains(statusErr.Message, "first query") {
			t.Fatalf("first query (existing=%v): %v", existing, err)
		}
	}
	post := func(req api.QueryRequest) string {
		body, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest("POST", "/api/query", bytes.NewReader(body)))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"type":"run.complete"`) {
			t.Fatalf("query failed: %d %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	post(api.QueryRequest{ChatID: chatID, AgentKey: "mock-agent", Message: "start"})
	if err := admit(selection); err != nil {
		t.Fatalf("follow-up rejected: %v", err)
	}
	for _, refs := range [][]api.Reference{{{Type: "file"}}, {{Type: "selection", Text: " "}}, {{Type: "site", URL: "https://example.com"}}} {
		if err := admit(refs); err == nil {
			t.Fatalf("empty/unsupported references accepted: %#v", refs)
		}
	}

	if err := admit(nil); err == nil {
		t.Fatal("empty query accepted after normal completion")
	}
	if err := completeServerFixtureRun(t, fixture.chats, chat.RunCompletion{ChatID: chatID, RunID: "failed-run", AgentKey: "mock-agent", FinishReason: "error", UpdatedAtMillis: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if err := admit(nil); err != nil {
		t.Fatalf("empty follow-up rejected: %v", err)
	}

	emptyBody := post(api.QueryRequest{ChatID: chatID, AgentKey: "mock-agent"})
	if strings.Contains(emptyBody, querymessages.EmptyQueryContinuation) {
		t.Fatal("synthetic instruction leaked into query events")
	}
	raw, err := fixture.chats.LoadRawMessages(chatID, 0)
	if err != nil {
		t.Fatal(err)
	}
	foundContinuation := false
	for _, message := range raw {
		if message["role"] == "user" && strings.Contains(fmt.Sprint(message["content"]), querymessages.EmptyQueryContinuation) {
			foundContinuation = true
		}
	}
	if !foundContinuation {
		t.Fatalf("continuation missing from model history: %#v", raw)
	}

	body := post(api.QueryRequest{ChatID: chatID, AgentKey: "mock-agent", References: selection})
	if !strings.Contains(body, "selected passage") {
		t.Fatalf("missing reference: %s", body)
	}
	for _, name := range []string{"page.html", "notes.md"} {
		if err := os.WriteFile(filepath.Join(fixture.chats.ChatDir(chatID), name), []byte("content"), 0600); err != nil {
			t.Fatal(err)
		}
		post(api.QueryRequest{ChatID: chatID, AgentKey: "mock-agent", References: []api.Reference{{Type: "file", URL: name}}})
	}
	detail, err := fixture.chats.LoadChat(chatID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range detail.Events {
		if event.Type == "request.query" && event.String("message") == "" && event.Value("references") != nil {
			found = true
		}
	}
	if !found {
		t.Fatal("reference-only query missing from replay")
	}
}

func TestReferenceOnlyFollowupQueryWebSocket(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`, `[DONE]`)
	}, testFixtureOptions{notifications: ws.NewHub()})
	server := newLoopbackServer(t, fixture.server)
	defer server.Close()
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	readConnectedPush(t, conn)
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for i, payload := range []map[string]any{
		{"chatId": "ws-followup", "agentKey": "mock-agent", "message": "start"},
		{"chatId": "ws-followup", "agentKey": "mock-agent"},
		{"chatId": "ws-followup", "agentKey": "mock-agent", "message": "", "references": []api.Reference{{Type: "selection", Text: "quote"}}},
	} {

		if i == 1 {
			if err := completeServerFixtureRun(t, fixture.chats, chat.RunCompletion{ChatID: "ws-followup", RunID: "canceled-run", AgentKey: "mock-agent", FinishReason: "cancel", UpdatedAtMillis: time.Now().UnixMilli()}); err != nil {
				t.Fatal(err)
			}
		}
		sendSelectionLaneRequest(t, conn, "query", "/api/query", payload)
		complete := false
		for {
			var frame ws.StreamFrame
			if err := conn.ReadJSON(&frame); err != nil {
				t.Fatal(err)
			}
			if frame.Event != nil && frame.Event.Type == "run.complete" {
				complete = true
			}
			if frame.Reason != "" {
				break
			}
		}
		if !complete {
			t.Fatalf("query %d did not complete", i)
		}
	}
}

func TestEmptyQueryRequiresFailedOrCanceledLastRun(t *testing.T) {
	for _, reason := range []string{"complete", "error", "cancel", "cancelled", "canceled", "interrupted", "unknown"} {
		t.Run(reason, func(t *testing.T) {
			fixture := newTestFixture(t)
			const chatID = "terminal-query"
			if _, _, err := fixture.chats.EnsureChat(chatID, "mock-agent", "start"); err != nil {
				t.Fatal(err)
			}
			if err := completeServerFixtureRun(t, fixture.chats, chat.RunCompletion{ChatID: chatID, RunID: "run-last", AgentKey: "mock-agent", FinishReason: reason, UpdatedAtMillis: time.Now().UnixMilli()}); err != nil {
				t.Fatal(err)
			}
			want := reason != "complete" && reason != "unknown"
			for _, path := range []string{"/api/chats", "/api/chat?chatId=" + chatID} {
				rec := httptest.NewRecorder()
				fixture.server.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
				if rec.Code != 200 || !strings.Contains(rec.Body.String(), fmt.Sprintf(`"canContinue":%t`, want)) {
					t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
				}
			}

			if !want {
				rec := httptest.NewRecorder()
				fixture.server.ServeHTTP(rec, httptest.NewRequest("POST", "/api/query", strings.NewReader(`{"chatId":"terminal-query","agentKey":"mock-agent"}`)))
				if rec.Code != 400 {
					t.Fatalf("empty HTTP query status=%d: %s", rec.Code, rec.Body.String())
				}
			}
			prepared, err := fixture.server.prepareQueryAdmissionRequest(t.Context(), api.QueryRequest{ChatID: chatID, AgentKey: "mock-agent"}, true, i18n.DefaultLocale, "http://example.com")
			releaseQuery(prepared.Release)
			if want && err != nil {
				t.Fatal(err)
			}
			if !want {
				var statusErr *statusError
				if !errors.As(err, &statusErr) || statusErr.Status != 400 || statusErr.Code != "empty_query_not_allowed" {
					t.Fatalf("unexpected rejection: %v", err)
				}
			}
		})
	}
}
