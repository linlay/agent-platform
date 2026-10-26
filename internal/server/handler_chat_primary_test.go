package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

func postPrimaryQuery(t *testing.T, handler http.Handler, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func putChatPrimary(t *testing.T, handler http.Handler, chatID string) *httptest.ResponseRecorder {
	t.Helper()
	data, _ := json.Marshal(api.SetPrimaryChatRequest{ChatID: chatID})
	req := httptest.NewRequest(http.MethodPut, "/api/chats/primary", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func agentPrimaryChatID(t *testing.T, handler http.Handler, agentKey string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/agents status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[[]api.AgentSummary]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	for _, agent := range response.Data {
		if agent.Key != agentKey {
			continue
		}
		if agent.PrimaryChat == nil {
			return ""
		}
		if !agent.PrimaryChat.Primary {
			t.Fatalf("primaryChat summary not marked primary: %+v", agent.PrimaryChat)
		}
		return agent.PrimaryChat.ChatID
	}
	t.Fatalf("agent %s not listed", agentKey)
	return ""
}

func TestPrimaryChatQueryCreatesAndReplacesPrimary(t *testing.T) {
	fixture := newTestFixture(t)
	if got := agentPrimaryChatID(t, fixture.server, "mock-agent"); got != "" {
		t.Fatalf("fresh instance has primary %q", got)
	}
	if rec := postPrimaryQuery(t, fixture.server, map[string]any{"chatId": "primary-a", "agentKey": "mock-agent", "message": "hello", "primary": true}); rec.Code != http.StatusOK {
		t.Fatalf("first primary query: %d %s", rec.Code, rec.Body.String())
	}
	if got := agentPrimaryChatID(t, fixture.server, "mock-agent"); got != "primary-a" {
		t.Fatalf("primary after first query = %q", got)
	}
	// An ordinary new Chat never takes over the pointer.
	if rec := postPrimaryQuery(t, fixture.server, map[string]any{"chatId": "plain", "agentKey": "mock-agent", "message": "hello"}); rec.Code != http.StatusOK {
		t.Fatalf("plain query: %d %s", rec.Code, rec.Body.String())
	}
	if rec := postPrimaryQuery(t, fixture.server, map[string]any{"chatId": "primary-b", "agentKey": "mock-agent", "message": "again", "primary": true}); rec.Code != http.StatusOK {
		t.Fatalf("second primary query: %d %s", rec.Code, rec.Body.String())
	}
	assertChatsLimitHTTP(t, fixture.server, "/api/chats?agentKey=mock-agent&primary=true", []string{"primary-b"})
	assertChatsLimitHTTP(t, fixture.server, "/api/chats?agentKey=mock-agent&primary=false", []string{"plain", "primary-a"})

	// Continuing a history Chat keeps the pointer where it is.
	if rec := postPrimaryQuery(t, fixture.server, map[string]any{"chatId": "primary-a", "message": "continue"}); rec.Code != http.StatusOK {
		t.Fatalf("history continue: %d %s", rec.Code, rec.Body.String())
	}
	if got := agentPrimaryChatID(t, fixture.server, "mock-agent"); got != "primary-b" {
		t.Fatalf("continuing history moved primary to %q", got)
	}
	// primary:true only applies to the query that creates the Chat.
	if rec := postPrimaryQuery(t, fixture.server, map[string]any{"chatId": "primary-a", "message": "again", "primary": true}); rec.Code != http.StatusBadRequest {
		t.Fatalf("primary on existing chat: %d %s", rec.Code, rec.Body.String())
	}

	rec := putChatPrimary(t, fixture.server, "primary-a")
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT primary: %d %s", rec.Code, rec.Body.String())
	}
	var put api.ApiResponse[map[string]any]
	if err := json.Unmarshal(rec.Body.Bytes(), &put); err != nil {
		t.Fatal(err)
	}
	if put.Data["changed"] != true || put.Data["previousChatId"] != "primary-b" || put.Data["agentKey"] != "mock-agent" {
		t.Fatalf("PUT response: %+v", put.Data)
	}
	if got := agentPrimaryChatID(t, fixture.server, "mock-agent"); got != "primary-a" {
		t.Fatalf("primary after PUT = %q", got)
	}
	if rec := putChatPrimary(t, fixture.server, "missing"); rec.Code != http.StatusNotFound {
		t.Fatalf("PUT missing chat: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPrimaryChatSwitchRejectedWhileCurrentPrimaryRuns(t *testing.T) {
	fixture := newTestFixture(t)
	if rec := postPrimaryQuery(t, fixture.server, map[string]any{"chatId": "primary-a", "agentKey": "mock-agent", "message": "hello", "primary": true}); rec.Code != http.StatusOK {
		t.Fatalf("first primary query: %d %s", rec.Code, rec.Body.String())
	}
	if rec := postPrimaryQuery(t, fixture.server, map[string]any{"chatId": "other", "agentKey": "mock-agent", "message": "hello"}); rec.Code != http.StatusOK {
		t.Fatalf("plain query: %d %s", rec.Code, rec.Body.String())
	}
	fixture.runs.Register(context.Background(), contracts.QuerySession{RunID: "busy-run", ChatID: "primary-a", AgentKey: "mock-agent", RunOwner: contracts.AgentRunOwner("mock-agent")})

	rec := postPrimaryQuery(t, fixture.server, map[string]any{"chatId": "primary-b", "agentKey": "mock-agent", "message": "new", "primary": true})
	if rec.Code != http.StatusConflict {
		t.Fatalf("primary query while busy: %d %s", rec.Code, rec.Body.String())
	}
	var rejected api.ApiResponse[map[string]any]
	if err := json.Unmarshal(rec.Body.Bytes(), &rejected); err != nil {
		t.Fatal(err)
	}
	info, _ := rejected.Data["error"].(map[string]any)
	if rejected.Msg != "primary_chat_busy" || info["chatId"] != "primary-a" {
		t.Fatalf("busy response: %s", rec.Body.String())
	}
	if summary, err := fixture.chats.Summary("primary-b"); err != nil || summary != nil {
		t.Fatalf("rejected query left a chat behind: %+v %v", summary, err)
	}
	if rec := putChatPrimary(t, fixture.server, "other"); rec.Code != http.StatusConflict {
		t.Fatalf("PUT while busy: %d %s", rec.Code, rec.Body.String())
	}

	fixture.runs.Finish("busy-run")
	if rec := postPrimaryQuery(t, fixture.server, map[string]any{"chatId": "primary-b", "agentKey": "mock-agent", "message": "new", "primary": true}); rec.Code != http.StatusOK {
		t.Fatalf("primary query after run ended: %d %s", rec.Code, rec.Body.String())
	}
	if got := agentPrimaryChatID(t, fixture.server, "mock-agent"); got != "primary-b" {
		t.Fatalf("primary = %q", got)
	}
}
