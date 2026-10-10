package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTEAMListedAsOrdinaryAgent(t *testing.T) {
	f := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, _ *http.Request) { writeProviderSSE(t, w, `[DONE]`) }, testFixtureOptions{setupRuntime: setupOrchestratedTeamRuntime(t)})
	store := f.chats.(*chat.FileStore)
	seedAgentModeChat(t, store, "chat-research-team", "loyw3v2a", "research", "TEAM", 2000)
	rec := httptest.NewRecorder()
	f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents?mode=TEAM&includeChats=1", nil))
	var response api.ApiResponse[[]api.AgentSummary]
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &response) != nil {
		t.Fatalf("response=%s", rec.Body.String())
	}
	if len(response.Data) != 1 || response.Data[0].Key != "research" || response.Data[0].Mode != "TEAM" || response.Data[0].Stats.TotalCount != 1 {
		t.Fatalf("agents=%#v", response.Data)
	}
	rec = httptest.NewRecorder()
	f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agent?agentKey=research", nil))
	var detail api.ApiResponse[api.AgentDetailResponse]
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &detail) != nil || detail.Data.TeamConfig == nil || len(detail.Data.TeamConfig.Members) != 2 {
		t.Fatalf("detail=%s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/teams", nil))
	if rec.Code != 404 {
		t.Fatalf("retired route status=%d", rec.Code)
	}
}
