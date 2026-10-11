package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"agent-platform/internal/api"
)

func TestKnowledgeBindingRequiredOnlyForKBASEAndCheckedAtManagement(t *testing.T) {
	for _, tc := range []struct {
		mode, binding string
		want          int
	}{
		{"KBASE", "", 400}, {"KBASE", "missing-library", 400},
		{"GENERAL", "", 200}, {"CODER", "", 200},
		{"GENERAL", "missing-library", 400}, {"CODER", "missing-library", 400},
		{"KBASE", "valid", 200}, {"GENERAL", "valid", 200}, {"CODER", "valid", 200},
	} {
		t.Run(tc.mode+"/"+tc.binding, func(t *testing.T) {
			f := newTestFixture(t)
			id := installTestKnowledgeLibrary(t, f.server)
			definition := map[string]any{"key": "candidate", "mode": tc.mode, "modelConfig": map[string]any{"modelKey": "mock-model"}, "runtimeConfig": map[string]any{"workspaceRoot": t.TempDir()}}
			if tc.binding != "" {
				binding := tc.binding
				if binding == "valid" {
					binding = id
				}
				definition["kbaseConfig"] = map[string]any{"libraryId": binding}
			}
			body, _ := json.Marshal(map[string]any{"key": "candidate", "definition": definition})
			w := httptest.NewRecorder()
			f.server.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/admin/agents/create", bytes.NewReader(body)))
			if w.Code != tc.want {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestNativeAgentsShareLibraryWithoutChangingWorkspaceOrSources(t *testing.T) {
	f := newTestFixture(t)
	id := installTestKnowledgeLibrary(t, f.server)
	before, err := f.server.deps.KBases.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ key, mode string }{
		{"reader-alpha", "KBASE"}, {"builder-beta", "CODER"}, {"research-gamma", "GENERAL"},
	} {
		workspace := t.TempDir()
		created := postAgentJSON[api.AgentDetailResponse](t, f.server, "/api/admin/agents/create", map[string]any{
			"key": tc.key,
			"definition": map[string]any{
				"key": tc.key, "name": tc.key, "mode": tc.mode, "modelConfig": map[string]any{"modelKey": "mock-model"},
				"runtimeConfig": map[string]any{"workspaceRoot": workspace},
				"kbaseConfig":   map[string]any{"libraryId": id},
			},
		})
		binding := created.Definition["kbaseConfig"].(map[string]any)
		if binding["libraryId"] != id {
			t.Fatalf("%s did not preserve its requested library binding", tc.key)
		}
		published, ok := f.registry.AgentDefinition(created.Key)
		if !ok {
			t.Fatalf("%s was not published", tc.key)
		}
		wanted, err := os.Stat(workspace)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := os.Stat(published.Workspace.Root)
		if err != nil || !os.SameFile(wanted, actual) {
			t.Fatalf("%s did not preserve its independent workspace: %+v %v", tc.key, published.Workspace, err)
		}
	}
	after, err := f.server.deps.KBases.Get(id)
	if err != nil || after.BaseRevision != before.BaseRevision {
		t.Fatalf("binding Agents changed the library source configuration: %+v %v", after, err)
	}
	libraries, err := f.server.deps.KBases.List()
	if err != nil || len(libraries) != 1 {
		t.Fatalf("shared binding created additional libraries: %+v %v", libraries, err)
	}
}

func TestKBASECombinedLibraryCreationAndUpdateRechecksBinding(t *testing.T) {
	f := newTestFixture(t)
	installTestKnowledgeLibrary(t, f.server)
	created := postAgentJSON[api.AgentDetailResponse](t, f.server, "/api/admin/agents/create", map[string]any{
		"createLibrary": map[string]any{"name": "Combined", "sourcePath": t.TempDir()},
		"definition":    map[string]any{"mode": "KBASE", "modelConfig": map[string]any{"modelKey": "mock-model"}, "runtimeConfig": map[string]any{"workspaceRoot": t.TempDir()}},
	})
	binding := created.Definition["kbaseConfig"].(map[string]any)
	id := binding["libraryId"].(string)
	if err := f.server.deps.KBases.ValidateBinding(id); err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{nil, map[string]any{"libraryId": "missing-library"}} {
		created.Definition["kbaseConfig"] = value
		body, _ := json.Marshal(map[string]any{"key": created.Key, "definition": created.Definition})
		w := httptest.NewRecorder()
		f.server.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/admin/agents/update", bytes.NewReader(body)))
		if w.Code != 400 {
			t.Fatalf("invalid update published: %d %s", w.Code, w.Body.String())
		}
		current, _ := f.registry.AgentDefinition(created.Key)
		if current.KBaseConfig.LibraryID != id {
			t.Fatal("failed validation changed binding")
		}
	}
}
