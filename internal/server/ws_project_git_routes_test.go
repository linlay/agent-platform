package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os/exec"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/ws"
)

func TestProjectGitWebSocket(t *testing.T) {
	fixture, _, workspace := newAgentFileTestFixture(t)
	cmd := exec.Command("git", "init", "-b", "knowledge-base")
	cmd.Dir = workspace
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	server := httptest.NewServer(fixture.server)
	defer server.Close()
	conn := dialTestWebSocket(t, server.URL)
	defer conn.Close()
	waitForPushFrameType(t, conn, "connected")
	sequence := 0
	request := func(route string, payload any, code int) json.RawMessage {
		t.Helper()
		sequence++
		id := fmt.Sprintf("git-%d", sequence)
		if err := conn.WriteJSON(ws.RequestFrame{Frame: ws.FrameRequest, Type: route, ID: id, Payload: marshalPayload(payload)}); err != nil {
			t.Fatal(err)
		}
		wire := waitForWebSocketFrame(t, conn, func(data []byte) bool {
			var frame ws.ResponseFrame
			return json.Unmarshal(data, &frame) == nil && frame.ID == id && (frame.Frame == ws.FrameResponse || frame.Frame == ws.FrameError)
		})
		var frame struct {
			Frame string          `json:"frame"`
			Code  int             `json:"code"`
			Data  json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(wire, &frame); err != nil || frame.Code != code {
			t.Fatalf("WS response: %s (%v), want %d", wire, err, code)
		}
		if code != 0 && frame.Frame != ws.FrameError {
			t.Fatalf("expected error: %s", wire)
		}
		return frame.Data
	}
	read := func(agent string) api.ProjectGitResponse {
		t.Helper()
		var result api.ProjectGitResponse
		if err := json.Unmarshal(request("/api/project/git", map[string]any{"agentKey": agent}, 0), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	if result := read("react-no-workspace"); result.Status != "no_workspace" {
		t.Fatalf("no workspace: %+v", result)
	}
	if result := read("coder-file"); result.Status != "not_repository" {
		t.Fatalf("not repo: %+v", result)
	}
	initial := read("kbase-file")
	if initial.Status != "branch" || initial.Branch != "knowledge-base" || initial.Revision == "" {
		t.Fatalf("snapshot: %+v", initial)
	}
	request("/api/project/git", map[string]any{}, 400)
	request("/api/project/git", map[string]any{"agentKey": "missing"}, 404)
	request("/api/project/git", map[string]any{"agentKey": 42}, 400)
	var list api.ProjectGitBranchesResponse
	if err := json.Unmarshal(request("/api/project/git/branches", map[string]any{"agentKey": "kbase-file"}, 0), &list); err != nil {
		t.Fatal(err)
	}
	if !list.CanChange || list.Git.Revision != initial.Revision || len(list.Branches) != 1 {
		t.Fatalf("list: %+v", list)
	}
	mutation := api.ProjectGitBranchRequest{AgentKey: "kbase-file", Operation: "create", Branch: "knowledge-new", ExpectedRevision: initial.Revision}
	var changed api.ProjectGitResponse
	if err := json.Unmarshal(request("/api/project/git/branches", mutation, 0), &changed); err != nil {
		t.Fatal(err)
	}
	if changed.Branch != "knowledge-new" || changed.Revision == initial.Revision {
		t.Fatalf("changed: %+v", changed)
	}
	// The same mutation cannot be replayed against stale HEAD.
	var failure struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(request("/api/project/git/branches", mutation, 409), &failure); err != nil || failure.Code != "revision_conflict" {
		t.Fatalf("conflict: %+v %v", failure, err)
	}
	for _, payload := range []any{
		map[string]any{"agentKey": "kbase-file", "operation": "delete"},
		map[string]any{"agentKey": "kbase-file", "operation": ""},
		map[string]any{"agentKey": "kbase-file", "operation": nil},
		map[string]any{"agentKey": "kbase-file", "branch": "accidental-write"},
		map[string]any{"agentKey": "kbase-file", "expectedRevision": initial.Revision},
		map[string]any{"agentKey": "kbase-file", "operation": 42},
	} {
		request("/api/project/git/branches", payload, 400)
	}
	if latest := read("kbase-file"); latest.Branch != changed.Branch {
		t.Fatalf("invalid writes changed HEAD: %+v", latest)
	}
}
