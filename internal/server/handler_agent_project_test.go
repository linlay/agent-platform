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
)

func TestCreateProjectRejectsInvalidWorkspaceBeforeWriting(t *testing.T) {
	workspace := t.TempDir()
	for _, root := range []string{"", "@root", filepath.VolumeName(workspace) + string(os.PathSeparator), "relative-project", filepath.Join(workspace, "missing")} {
		t.Run(root, func(t *testing.T) {
			fixture := newTestFixture(t)
			body, _ := json.Marshal(map[string]any{
				"key": "project-candidate", "isProject": true,
				"definition": map[string]any{
					"mode": "GENERAL", "modelConfig": map[string]any{"modelKey": "mock-model"},
					"runtimeConfig": map[string]any{"workspaceRoot": root},
				},
			})
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/agents/create", bytes.NewReader(body)))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_project_workspace") {
				t.Fatalf("unexpected response: %d %s", rec.Code, rec.Body.String())
			}
			if _, err := os.Stat(filepath.Join(fixture.server.deps.Config.Paths.AgentsDir, "project-candidate")); !os.IsNotExist(err) {
				t.Fatalf("invalid project wrote an Agent directory: %v", err)
			}
		})
	}
}

func TestCreateProjectPublishesWorkspaceIdentityWithoutPersistingIntent(t *testing.T) {
	for _, mode := range []string{"GENERAL", "CODER"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newTestFixture(t)
			workspace := t.TempDir()
			created := postAgentJSON[api.AgentDetailResponse](t, fixture.server, "/api/admin/agents/create", map[string]any{
				"isProject": true,
				"definition": map[string]any{
					"mode": mode, "modelConfig": map[string]any{"modelKey": "mock-model"},
					"runtimeConfig": map[string]any{"workspaceRoot": workspace},
				},
			})
			content, err := os.ReadFile(created.Source.Path)
			if err != nil || strings.Contains(string(content), "isProject") {
				t.Fatalf("creation intent persisted in Agent source: %s, %v", content, err)
			}
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents?hasWorkspace=true", nil))
			var response api.ApiResponse[[]api.AgentSummary]
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || rec.Code != http.StatusOK {
				t.Fatalf("project catalog: %d %s, %v", rec.Code, rec.Body.String(), err)
			}
			for _, agent := range response.Data {
				if agent.Key == created.Key && agent.WorkspaceDir == workspace {
					return
				}
			}
			t.Fatalf("created Agent missing from Projects: %#v", response.Data)
		})
	}
}

func TestOrdinaryAgentCreateRetainsRootWorkspace(t *testing.T) {
	for _, intent := range []any{nil, false} {
		fixture := newTestFixture(t)
		request := map[string]any{"definition": map[string]any{
			"mode": "GENERAL", "modelConfig": map[string]any{"modelKey": "mock-model"},
			"runtimeConfig": map[string]any{"workspaceRoot": "@root"},
		}}
		if intent != nil {
			request["isProject"] = intent
		}
		created := postAgentJSON[api.AgentDetailResponse](t, fixture.server, "/api/admin/agents/create", request)
		definition, ok := fixture.server.deps.Registry.AgentDefinition(created.Key)
		if !ok || !definition.Workspace.HostRoot || definition.Workspace.ProjectDir() != "" {
			t.Fatalf("ordinary Agent root identity changed: %#v", definition.Workspace)
		}
	}
}
