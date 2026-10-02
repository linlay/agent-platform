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

func TestChatsHasWorkspaceSeparatesChatAndProjectAgents(t *testing.T) {
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
		"":                                        "chat-team,chat-project-new,chat-general,chat-project-old",
		"hasWorkspace=false":                      "chat-team,chat-general",
		"hasWorkspace=true":                       "chat-project-new,chat-project-old",
		"hasWorkspace=false&mode=GENERAL":         "chat-team,chat-general",
		"hasWorkspace=true&limit=1":               "chat-project-new",
		"hasWorkspace=false&limit=1":              "chat-team",
		"hasWorkspace=true&mode=GENERAL":          "chat-project-new,chat-project-old",
		"hasWorkspace=false&pinned=false&limit=1": "chat-team",
		"hasWorkspace=true&pinned=false&limit=1":  "chat-project-new",
	} {
		if code, got := list(query); code != http.StatusOK || got != want {
			t.Fatalf("/api/chats?%s returned %d %q, want %q", query, code, got, want)
		}
	}
	for _, query := range []string{"hasWorkspace=project", "hasWorkspace=", "agentType=chat"} {
		if code, _ := list(query); code != http.StatusBadRequest {
			t.Fatalf("/api/chats?%s must be rejected, got %d", query, code)
		}
	}
}

func TestChatsPinnedFilterReadsPinsAndSkipsThemFromRecentPage(t *testing.T) {
	fixture := newTestFixture(t)
	store, ok := fixture.chats.(*chat.FileStore)
	if !ok {
		t.Fatalf("expected file chat store, got %T", fixture.chats)
	}
	seedAgentModeChat(t, store, "chat-a", "loyw3v21", "chat-only-agent", "", "GENERAL", 1_000)
	seedAgentModeChat(t, store, "chat-b", "loyw3v22", "chat-only-agent", "", "GENERAL", 2_000)
	seedAgentModeChat(t, store, "chat-c", "loyw3v23", "chat-only-agent", "", "GENERAL", 3_000)
	seedAgentModeChat(t, store, "chat-d", "loyw3v24", "chat-only-agent", "", "GENERAL", 4_000)
	for _, chatID := range []string{"chat-a", "chat-d"} {
		if _, _, err := store.SetChatPinned(chatID, true); err != nil {
			t.Fatalf("pin %s: %v", chatID, err)
		}
	}
	pins, err := store.ChatPinned()
	if err != nil {
		t.Fatalf("read pins: %v", err)
	}

	list := func(query string) string {
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chats?"+query, nil))
		var response api.ApiResponse[[]api.ChatSummaryResponse]
		_ = json.Unmarshal(rec.Body.Bytes(), &response)
		if rec.Code != http.StatusOK {
			t.Fatalf("/api/chats?%s returned %d", query, rec.Code)
		}
		return strings.Join(apiChatIDs(response.Data), ",")
	}
	if got, want := list("pinned=true"), strings.Join(pins.Order, ","); got != want {
		t.Fatalf("pinned chats = %q, want pin order %q", got, want)
	}
	// The page is filled after pins are skipped, not cut short by them.
	if got := list("pinned=false&limit=2"); got != "chat-c,chat-b" {
		t.Fatalf("unpinned page = %q, want chat-c,chat-b", got)
	}

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chats/order", nil))
	var order api.ApiResponse[api.ChatOrderSnapshotResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &order); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("/api/chats/order returned %d: %v", rec.Code, err)
	}
	if got, want := strings.Join(apiChatIDs(order.Data.PinnedChats), ","), strings.Join(pins.Order, ","); got != want {
		t.Fatalf("order snapshot pinned chats = %q, want %q", got, want)
	}
}

func TestAgentsHasWorkspaceSelectsProjectAgents(t *testing.T) {
	fixture := newTestFixture(t)

	list := func(query string) (int, []api.AgentSummary) {
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents?"+query, nil))
		var response api.ApiResponse[[]api.AgentSummary]
		_ = json.Unmarshal(rec.Body.Bytes(), &response)
		return rec.Code, response.Data
	}
	_, all := list("")
	code, projects := list("hasWorkspace=true")
	if code != http.StatusOK || len(projects) == 0 {
		t.Fatalf("/api/agents?hasWorkspace=true returned %d with %d agents", code, len(projects))
	}
	for _, agent := range projects {
		if strings.TrimSpace(agent.WorkspaceDir) == "" {
			t.Fatalf("agent %q has no workspace but was listed as a project", agent.Key)
		}
	}
	code, chatAgents := list("hasWorkspace=false")
	if code != http.StatusOK {
		t.Fatalf("/api/agents?hasWorkspace=false returned %d", code)
	}
	for _, agent := range chatAgents {
		if strings.TrimSpace(agent.WorkspaceDir) != "" {
			t.Fatalf("agent %q has a workspace but was listed without one", agent.Key)
		}
	}
	if len(projects)+len(chatAgents) != len(all) {
		t.Fatalf("hasWorkspace split %d+%d does not cover %d agents", len(projects), len(chatAgents), len(all))
	}
	if code, _ := list("hasWorkspace=yes"); code != http.StatusBadRequest {
		t.Fatalf("non-boolean hasWorkspace must be rejected, got %d", code)
	}
}
