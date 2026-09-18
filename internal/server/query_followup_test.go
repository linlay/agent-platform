package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/i18n"
	"agent-platform/internal/ws"
	gws "github.com/gorilla/websocket"
)

func TestReferenceOnlyQueryRequiresMainHistory(t *testing.T) {
	fixture := newTestFixture(t)
	const chatID = "query-followup"
	selection := []api.Reference{{Type: "selection", Text: "selected passage"}}
	admit := func(refs []api.Reference) error {
		prepared, err := fixture.server.prepareQueryAdmissionRequest(t.Context(), api.QueryRequest{ChatID: chatID, AgentKey: "mock-agent", References: refs}, true, i18n.DefaultLocale, "http://example.com")
		releaseQuery(prepared.release)
		return err
	}
	for _, existing := range []bool{false, true} {
		if existing {
			if _, _, err := fixture.chats.EnsureChat(chatID, "mock-agent", "", ""); err != nil {
				t.Fatal(err)
			}
		}
		err := admit(selection)
		var statusErr *statusError
		if !errors.As(err, &statusErr) || statusErr.status != 400 || !strings.Contains(statusErr.message, "first query") {
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
	for _, refs := range [][]api.Reference{nil, {{Type: "file"}}, {{Type: "selection", Text: " "}}, {{Type: "site", URL: "https://example.com"}}} {
		if err := admit(refs); err == nil {
			t.Fatalf("empty/unsupported references accepted: %#v", refs)
		}
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
		{"chatId": "ws-followup", "agentKey": "mock-agent", "message": "", "references": []api.Reference{{Type: "selection", Text: "quote"}}},
	} {
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
