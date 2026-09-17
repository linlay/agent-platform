package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
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

func TestProjectGitBranchesHTTP(t *testing.T) {
	fixture, _, workspace := newAgentFileTestFixture(t)
	cmd := exec.Command("git", "init", "-b", "knowledge-base")
	cmd.Dir = workspace
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/project/git/branches?agentKey=kbase-file", nil))
	var listed api.ApiResponse[api.ProjectGitBranchesResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || !listed.Data.CanChange || listed.Data.Git.Revision == "" || len(listed.Data.Branches) != 1 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	request := api.ProjectGitBranchRequest{AgentKey: "kbase-file", Operation: "create", Branch: "knowledge-new", ExpectedRevision: listed.Data.Git.Revision}
	payload, _ := json.Marshal(request)
	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/project/git/branches", bytes.NewReader(payload)))
	var changed api.ApiResponse[api.ProjectGitResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &changed); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || changed.Data.Branch != "knowledge-new" {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/project/git/branches", bytes.NewReader(payload)))
	if rec.Code != 409 {
		t.Fatalf("stale create: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/project/git/branches", nil))
	if rec.Code != 405 {
		t.Fatalf("method: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/project/git/branches", strings.NewReader(`{"agentKey":"kbase-file","operation":"delete","branch":"knowledge-new"}`)))
	if rec.Code != 400 {
		t.Fatalf("invalid operation: %d", rec.Code)
	}
}
