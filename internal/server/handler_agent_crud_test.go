package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

func TestAgentHTTPCRUDAndEditableDetail(t *testing.T) {
	fixture := newTestFixture(t)

	created := postAgentJSON[api.AgentDetailResponse](t, fixture.server, "/api/agent/create", map[string]any{
		"key": "editable-agent",
		"definition": map[string]any{
			"key":         "editable-agent",
			"name":        "Editable Agent",
			"role":        "Editor",
			"description": "editable test agent",
			"mode":        "REACT",
			"modelConfig": map[string]any{"modelKey": "mock-model"},
			"toolConfig":  map[string]any{"tools": []any{"datetime"}},
			"runtimeConfig": map[string]any{
				"environmentId": "shell",
				"level":         "RUN",
				"env":           map[string]any{"HTTP_PROXY": "http://agent-proxy"},
			},
		},
		"soulPrompt":   "Soul v1",
		"agentsPrompt": "Agents v1",
	})
	if created.Key != "editable-agent" || created.Source == nil || created.Source.Kind != "directory" {
		t.Fatalf("unexpected create response %#v", created)
	}
	if created.SoulPrompt != "Soul v1" || created.AgentsPrompt != "Agents v1" {
		t.Fatalf("expected prompts in response, got %#v", created)
	}
	if created.Definition["key"] != "editable-agent" {
		t.Fatalf("expected editable definition, got %#v", created.Definition)
	}
	runtimeConfig, _ := created.Definition["runtimeConfig"].(map[string]any)
	env, _ := runtimeConfig["env"].(map[string]any)
	if env["HTTP_PROXY"] != "http://agent-proxy" {
		t.Fatalf("expected runtime env to be returned in editable detail, got %#v", created.Definition)
	}
	if !strings.HasSuffix(created.Source.Path, filepath.Join("editable-agent", "agent.yml")) {
		t.Fatalf("unexpected source path %q", created.Source.Path)
	}

	detail := getAgentDetail(t, fixture.server, "editable-agent")
	if detail.SoulPrompt != "Soul v1" || detail.AgentsPrompt != "Agents v1" {
		t.Fatalf("expected prompts from detail, got %#v", detail)
	}

	updatedDefinition := detail.Definition
	updatedDefinition["description"] = "updated test agent"
	updated := postAgentJSON[api.AgentDetailResponse](t, fixture.server, "/api/agent/update", map[string]any{
		"key":          "editable-agent",
		"definition":   updatedDefinition,
		"agentsPrompt": "Agents v2",
	})
	if updated.Description != "updated test agent" || updated.SoulPrompt != "Soul v1" || updated.AgentsPrompt != "Agents v2" {
		t.Fatalf("unexpected update response %#v", updated)
	}

	deleted := postAgentJSON[map[string]any](t, fixture.server, "/api/agent/delete", map[string]any{"key": "editable-agent"})
	if deleted["key"] != "editable-agent" || deleted["deleted"] != true {
		t.Fatalf("unexpected delete response %#v", deleted)
	}
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agent?agentKey=editable-agent", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected deleted agent to be absent, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAgentProxyCRUDAllowsProxyConfigWithoutModelConfig(t *testing.T) {
	fixture := newTestFixture(t)

	created := postAgentJSON[api.AgentDetailResponse](t, fixture.server, "/api/agent/create", map[string]any{
		"key": "proxy-agent",
		"definition": map[string]any{
			"key":         "proxy-agent",
			"name":        "Proxy Agent",
			"role":        "Proxy",
			"description": "proxy test agent",
			"mode":        "PROXY",
			"proxyConfig": map[string]any{
				"baseUrl":   "http://127.0.0.1:3210",
				"token":     "proxy-token",
				"timeoutMs": 300000,
			},
		},
	})
	proxyConfig, _ := created.Definition["proxyConfig"].(map[string]any)
	if created.Mode != "PROXY" || proxyConfig["token"] != "proxy-token" {
		t.Fatalf("expected editable proxy detail with token, got %#v", created)
	}
	if created.Definition["mode"] != "PROXY" {
		t.Fatalf("expected PROXY to persist as PROXY, got %#v", created.Definition)
	}
}

func TestAgentPlanExecuteCRUDUsesAPIModeContract(t *testing.T) {
	fixture := newTestFixture(t)

	created := postAgentJSON[api.AgentDetailResponse](t, fixture.server, "/api/agent/create", map[string]any{
		"key": "plan-agent",
		"definition": map[string]any{
			"key":         "plan-agent",
			"name":        "Plan Agent",
			"role":        "Planner",
			"description": "plan execute test agent",
			"mode":        "PLAN-EXECUTE",
			"modelConfig": map[string]any{"modelKey": "mock-model"},
		},
	})
	if created.Mode != "PLAN-EXECUTE" || created.Definition["mode"] != "PLAN-EXECUTE" {
		t.Fatalf("expected PLAN-EXECUTE create response, got %#v", created)
	}

	detail := getAgentDetail(t, fixture.server, "plan-agent")
	if detail.Mode != "PLAN-EXECUTE" || detail.Definition["mode"] != "PLAN-EXECUTE" {
		t.Fatalf("expected PLAN-EXECUTE detail response, got %#v", detail)
	}

	detail.Definition["description"] = "updated plan execute test agent"
	updated := postAgentJSON[api.AgentDetailResponse](t, fixture.server, "/api/agent/update", map[string]any{
		"key":        "plan-agent",
		"definition": detail.Definition,
	})
	if updated.Mode != "PLAN-EXECUTE" || updated.Definition["mode"] != "PLAN-EXECUTE" {
		t.Fatalf("expected PLAN-EXECUTE update response, got %#v", updated)
	}
}

func TestAgentCreateCoderAndOpenWorkspace(t *testing.T) {
	fixture := newTestFixture(t)
	workspaceDir := t.TempDir()

	created := postAgentJSON[api.AgentDetailResponse](t, fixture.server, "/api/agent/create", map[string]any{
		"key": "coder-project",
		"definition": map[string]any{
			"key":  "coder-project",
			"name": "coder-project",
			"mode": "CODER",
			"workspace": map[string]any{
				"root": workspaceDir,
			},
			"runtimeConfig": map[string]any{
				"workspaceRoot": workspaceDir,
			},
			"visibility": map[string]any{
				"scopes": []any{"nav", "copilot"},
			},
		},
	})
	if created.Key != "coder-project" || created.Mode != "CODER" {
		t.Fatalf("unexpected coder create response %#v", created)
	}
	if _, ok := created.Definition["workspace"]; ok {
		t.Fatalf("coder definition should not persist legacy workspace root, got %#v", created.Definition)
	}
	visibility, _ := created.Definition["visibility"].(map[string]any)
	scopes, _ := visibility["scopes"].([]any)
	if len(scopes) != 1 || scopes[0] != "nav" {
		t.Fatalf("coder visibility scopes = %#v, want [nav]", visibility["scopes"])
	}
	if created.Source == nil {
		t.Fatalf("expected created source")
	}
	data, err := os.ReadFile(created.Source.Path)
	if err != nil {
		t.Fatalf("read created agent file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 3 || lines[0] != "key: coder-project" || lines[1] != "name: coder-project" || lines[2] != "mode: CODER" {
		t.Fatalf("unexpected YAML header order:\n%s", data)
	}
	if strings.Contains(string(data), "\nworkspace:") || strings.Contains(string(data), "- copilot") {
		t.Fatalf("created coder file should omit workspace and copilot scope:\n%s", data)
	}

	var openedPath string
	previousOpen := openWorkspacePath
	openWorkspacePath = func(path string) error {
		openedPath = path
		return nil
	}
	t.Cleanup(func() { openWorkspacePath = previousOpen })

	opened := postAgentJSON[api.OpenAgentWorkspaceResponse](t, fixture.server, "/api/agent/open-workspace", map[string]any{
		"agentKey": "coder-project",
	})
	if !opened.Opened || opened.WorkspaceDir != workspaceDir || openedPath != workspaceDir {
		t.Fatalf("unexpected open response=%#v openedPath=%q", opened, openedPath)
	}
}

func TestAgentOpenWorkspaceRejectsUnknownWorkspace(t *testing.T) {
	fixture := newTestFixture(t)
	rec := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"workspaceDir":"/tmp/not-an-agent-workspace"}`)
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/agent/open-workspace", body))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAgentEditorOptionsHTTP(t *testing.T) {
	fixture := newTestFixture(t)

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agent/editor-options", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("options returned %d: %s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[api.AgentEditorOptionsResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode options response: %v", err)
	}
	if len(response.Data.Models) != 1 || response.Data.Models[0].Key != "mock-model" {
		t.Fatalf("expected mock model option, got %#v", response.Data.Models)
	}
	if got := response.Data.Modes; len(got) != 4 ||
		got[0].Key != "REACT" || got[0].Label != "REACT" ||
		got[1].Key != "PLAN-EXECUTE" || got[1].Label != "PLAN-EXECUTE" ||
		got[2].Key != "CODER" || got[2].Label != "CODER" ||
		got[3].Key != "PROXY" || got[3].Label != "PROXY" {
		t.Fatalf("unexpected modes %#v", got)
	}
	if len(response.Data.ContextTags) != 4 || response.Data.ContextTags[0].Key != "system" || response.Data.ContextTags[3].Key != "all-agents" {
		t.Fatalf("unexpected context tags %#v", response.Data.ContextTags)
	}
	if response.Data.ProxyConfigSchema.DefaultTimeoutMs != 300000 || len(response.Data.ProxyConfigSchema.Fields) != 6 || !response.Data.ProxyConfigSchema.Fields[0].Required {
		t.Fatalf("unexpected proxy schema %#v", response.Data.ProxyConfigSchema)
	}
}

func TestAgentCRUDSafetyErrors(t *testing.T) {
	fixture := newTestFixture(t)

	cases := []struct {
		name   string
		path   string
		body   map[string]any
		status int
	}{
		{
			name: "duplicate",
			path: "/api/agent/create",
			body: map[string]any{
				"key": "mock-agent",
				"definition": map[string]any{
					"key":         "mock-agent",
					"name":        "Duplicate",
					"description": "duplicate",
				},
			},
			status: http.StatusConflict,
		},
		{
			name: "missing key",
			path: "/api/agent/create",
			body: map[string]any{
				"definition": map[string]any{"key": "", "name": "Missing"},
			},
			status: http.StatusBadRequest,
		},
		{
			name: "path traversal",
			path: "/api/agent/create",
			body: map[string]any{
				"key": "../bad",
				"definition": map[string]any{
					"key":         "../bad",
					"name":        "Bad",
					"description": "bad",
				},
			},
			status: http.StatusBadRequest,
		},
		{
			name: "mismatched definition key",
			path: "/api/agent/create",
			body: map[string]any{
				"key": "safe-key",
				"definition": map[string]any{
					"key":         "other-key",
					"name":        "Safe",
					"description": "safe",
				},
			},
			status: http.StatusBadRequest,
		},
		{
			name: "proxy missing base url",
			path: "/api/agent/create",
			body: map[string]any{
				"key": "bad-proxy",
				"definition": map[string]any{
					"key":         "bad-proxy",
					"name":        "Bad Proxy",
					"description": "bad proxy",
					"mode":        "PROXY",
					"proxyConfig": map[string]any{"token": "token"},
				},
			},
			status: http.StatusBadRequest,
		},
		{
			name:   "delete missing",
			path:   "/api/agent/delete",
			body:   map[string]any{"key": "missing-agent"},
			status: http.StatusNotFound,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(tc.body)
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, tc.path, bytes.NewReader(body)))
			if rec.Code != tc.status {
				t.Fatalf("expected %d, got %d: %s", tc.status, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestAgentWSCRUDMirrorHTTP(t *testing.T) {
	hub := ws.NewHub()
	t.Cleanup(func() { hub.CloseAll(gws.CloseNormalClosure, "test done") })
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: hub,
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 4
			cfg.WebSocket.PingIntervalMs = 30000
		},
	})
	server := httptest.NewServer(fixture.server)
	defer server.Close()

	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()
	readAutomationConnectedPush(t, conn)

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/agent/editor-options",
		ID:    "agent-options",
	}); err != nil {
		t.Fatalf("write options request: %v", err)
	}
	var optionsFrame ws.ResponseFrame
	if err := conn.ReadJSON(&optionsFrame); err != nil {
		t.Fatalf("read options response: %v", err)
	}
	options, err := marshalAgentResponseData[api.AgentEditorOptionsResponse](optionsFrame.Data)
	if err != nil {
		t.Fatalf("decode options data: %v", err)
	}
	if optionsFrame.Frame != ws.FrameResponse || optionsFrame.ID != "agent-options" ||
		len(options.Modes) != 4 || options.Modes[1].Key != "PLAN-EXECUTE" || options.Modes[3].Key != "PROXY" {
		t.Fatalf("unexpected options frame %#v data=%#v", optionsFrame, options)
	}

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/agent/create",
		ID:    "create-agent",
		Payload: ws.MarshalPayload(map[string]any{
			"key": "ws-agent",
			"definition": map[string]any{
				"key":         "ws-agent",
				"name":        "WS Agent",
				"role":        "WS",
				"description": "ws test agent",
				"mode":        "REACT",
				"modelConfig": map[string]any{"modelKey": "mock-model"},
			},
			"soulPrompt": "WS Soul",
		}),
	}); err != nil {
		t.Fatalf("write create request: %v", err)
	}
	var createFrame ws.ResponseFrame
	if err := conn.ReadJSON(&createFrame); err != nil {
		t.Fatalf("read create response: %v", err)
	}
	created, err := marshalAgentResponseData[api.AgentDetailResponse](createFrame.Data)
	if err != nil {
		t.Fatalf("decode create data: %v", err)
	}
	if createFrame.Frame != ws.FrameResponse || createFrame.ID != "create-agent" || created.Key != "ws-agent" || created.SoulPrompt != "WS Soul" {
		t.Fatalf("unexpected create frame %#v data=%#v", createFrame, created)
	}

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    "/api/agent/delete",
		ID:      "delete-agent",
		Payload: ws.MarshalPayload(map[string]any{"key": "ws-agent"}),
	}); err != nil {
		t.Fatalf("write delete request: %v", err)
	}
	var deleteFrame ws.ResponseFrame
	if err := conn.ReadJSON(&deleteFrame); err != nil {
		t.Fatalf("read delete response: %v", err)
	}
	deleted, err := marshalAgentResponseData[map[string]any](deleteFrame.Data)
	if err != nil {
		t.Fatalf("decode delete data: %v", err)
	}
	if deleteFrame.Frame != ws.FrameResponse || deleteFrame.ID != "delete-agent" || deleted["deleted"] != true {
		t.Fatalf("unexpected delete frame %#v data=%#v", deleteFrame, deleted)
	}
}

func getAgentDetail(t *testing.T, server *Server, key string) api.AgentDetailResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agent?agentKey="+key, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("agent detail returned %d: %s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[api.AgentDetailResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode detail response: %v", err)
	}
	return response.Data
}

func postAgentJSON[T any](t *testing.T, server *Server, path string, payload any) T {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s returned %d: %s", path, rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[T]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return response.Data
}

func marshalAgentResponseData[T any](value any) (T, error) {
	var out T
	data, err := json.Marshal(value)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, err
	}
	return out, nil
}
