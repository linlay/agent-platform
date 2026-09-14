package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"

	"agent-platform/internal/api"
)

func TestProjectGitHTTP(t *testing.T) {
	fixture, _, workspace := newAgentFileTestFixture(t)
	cmd := exec.Command("git", "init", "-b", "knowledge-branch")
	cmd.Dir = workspace
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/project/git?agentKey=kbase-file", nil))
	var response api.ApiResponse[api.ProjectGitResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || response.Code != 0 || response.Data.Status != "branch" || response.Data.Branch != "knowledge-branch" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response: %d %s", rec.Code, rec.Body.String())
	}
	for _, tc := range []struct {
		method, query string
		status        int
	}{
		{http.MethodGet, "", 400}, {http.MethodGet, "?agentKey=missing", 404}, {http.MethodPost, "?agentKey=kbase-file", 405},
	} {
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(tc.method, "/api/project/git"+tc.query, nil))
		if rec.Code != tc.status {
			t.Fatalf("%+v: %d %s", tc, rec.Code, rec.Body.String())
		}
	}
}
