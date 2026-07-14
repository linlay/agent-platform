package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

func TestTeamOwnedRunControlUsesTeamIDAndHidesExecutionAgent(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	})
	runs := fixture.runs.(*contracts.InMemoryRunManager)
	_, _, _ = runs.Register(context.Background(), contracts.QuerySession{
		RunID:    "run-team-control",
		ChatID:   "chat-team-control",
		AgentKey: "__team_coordinator",
		TeamID:   "team-a",
		RunOwner: contracts.TeamRunOwner("team-a", "__team_coordinator"),
	})

	status, ok := runs.RunStatus("run-team-control")
	if !ok {
		t.Fatal("run status not found")
	}
	if !contracts.IsTeamRunOwner(status.AgentKey, status.TeamID) || status.AgentKey != "" || status.TeamID != "team-a" {
		t.Fatalf("unexpected public run owner %#v", status)
	}
	if status.ExecutionAgentKey != "__team_coordinator" {
		t.Fatalf("execution agent key = %q", status.ExecutionAgentKey)
	}
	public := toAPIActiveRunInfo(status)
	if public.TeamID != "team-a" || public.AgentKey != "" {
		t.Fatalf("unexpected API run owner %#v", public)
	}

	tests := []struct {
		name   string
		method string
		path   string
		body   string
		status int
	}{
		{
			name:   "attach requires teamId",
			method: http.MethodGet,
			path:   "/api/attach?runId=run-team-control",
			status: http.StatusBadRequest,
		},
		{
			name:   "attach rejects wrong teamId",
			method: http.MethodGet,
			path:   "/api/attach?runId=run-team-control&teamId=team-b",
			status: http.StatusForbidden,
		},
		{
			name:   "attach rejects execution agent key",
			method: http.MethodGet,
			path:   "/api/attach?runId=run-team-control&teamId=team-a&agentKey=__team_coordinator",
			status: http.StatusBadRequest,
		},
		{
			name:   "submit requires teamId",
			method: http.MethodPost,
			path:   "/api/submit",
			body:   `{"runId":"run-team-control","awaitingId":"await-team","params":[]}`,
			status: http.StatusBadRequest,
		},
		{
			name:   "submit rejects wrong teamId",
			method: http.MethodPost,
			path:   "/api/submit",
			body:   `{"teamId":"team-b","runId":"run-team-control","awaitingId":"await-team","params":[]}`,
			status: http.StatusForbidden,
		},
		{
			name:   "steer accepts team owner",
			method: http.MethodPost,
			path:   "/api/steer",
			body:   `{"teamId":"team-a","runId":"run-team-control","message":"continue"}`,
			status: http.StatusOK,
		},
		{
			name:   "access level accepts team owner",
			method: http.MethodPost,
			path:   "/api/access-level",
			body:   `{"teamId":"team-a","runId":"run-team-control","accessLevel":"auto_approve"}`,
			status: http.StatusOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body))
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("expected %d, got %d: %s", tc.status, rec.Code, rec.Body.String())
			}
		})
	}

	if statusErr := fixture.server.validateSubmitOwner(api.SubmitRequest{
		RunID:      "run-team-control",
		TeamID:     "team-a",
		AwaitingID: "await-team",
	}); statusErr != nil {
		t.Fatalf("matching team submit identity rejected: %v", statusErr)
	}
}

func TestTeamOwnedRunInterruptAcceptsOnlyTeamOwner(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	})
	runs := fixture.runs.(*contracts.InMemoryRunManager)
	_, _, _ = runs.Register(context.Background(), contracts.QuerySession{
		RunID:    "run-team-interrupt",
		ChatID:   "chat-team-interrupt",
		AgentKey: "__team_coordinator",
		TeamID:   "team-a",
		RunOwner: contracts.TeamRunOwner("team-a", "__team_coordinator"),
	})

	req := httptest.NewRequest(http.MethodPost, "/api/interrupt", bytes.NewBufferString(`{"teamId":"team-a","runId":"run-team-interrupt"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}
