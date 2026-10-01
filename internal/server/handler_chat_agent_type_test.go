package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
)

func TestChatsAgentTypeSeparatesChatAndProjectAgents(t *testing.T) {
	fixture := newTestFixture(t)
	store, ok := fixture.chats.(*chat.FileStore)
	if !ok {
		t.Fatalf("expected file chat store, got %T", fixture.chats)
	}
	// mock-agent has a specific Workspace in the fixture, so it is a project
	// agent. An agent that is not in the catalog cannot be a project agent.
	seedAgentModeChat(t, store, "chat-project-old", "loyw3v21", "mock-agent", "", "REACT", 1_000)
	seedAgentModeChat(t, store, "chat-general", "loyw3v22", "chat-only-agent", "", "GENERAL", 2_000)
	seedAgentModeChat(t, store, "chat-project-new", "loyw3v23", "mock-agent", "", "GENERAL", 3_000)
	seedAgentModeChat(t, store, "chat-team", "loyw3v24", "", "team-a", "TEAM", 4_000)

	list := func(query string) (int, string) {
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chats?"+query, nil))
		var response api.ApiResponse[[]api.ChatSummaryResponse]
		_ = json.Unmarshal(rec.Body.Bytes(), &response)
		return rec.Code, strings.Join(apiChatIDs(response.Data), ",")
	}
	for query, want := range map[string]string{
		"":                               "chat-team,chat-project-new,chat-general,chat-project-old",
		"agentType=chat":                 "chat-team,chat-general",
		"agentType=project":              "chat-project-new,chat-project-old",
		"agentType=CHAT&mode=GENERAL":    "chat-team,chat-general",
		"agentType=project&limit=1":      "chat-project-new",
		"agentType=chat&limit=1":         "chat-team",
		"agentType=project&mode=GENERAL": "chat-project-new,chat-project-old",
	} {
		if code, got := list(query); code != http.StatusOK || got != want {
			t.Fatalf("/api/chats?%s returned %d %q, want %q", query, code, got, want)
		}
	}
	if code, _ := list("agentType=workspace"); code != http.StatusBadRequest {
		t.Fatalf("unknown agentType must be rejected, got %d", code)
	}
}
