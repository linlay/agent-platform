package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
	"agent-platform/internal/kbasescenter"
)

func TestCreateAgentWithLibraryAndReferencedDelete(t *testing.T) {
	f := newTestFixture(t)
	center, err := kbasescenter.New(context.Background(), t.TempDir(), t.TempDir(), nil, kbasescenter.Options{References: func(id string) []string {
		refs := []string{}
		for _, a := range f.registry.(interface{ AdminAgents() []catalog.AdminAgent }).AdminAgents() {
			if contracts.AnyMapNode(a.Definition["kbaseConfig"])["libraryId"] == id {
				refs = append(refs, a.Key)
			}
		}
		return refs
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer center.Close(context.Background())
	f.server.deps.KBasesCenter = center
	source, workspace := t.TempDir(), t.TempDir()
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	request := map[string]any{"key": "library-owner", "isProject": true, "createLibrary": map[string]any{"name": "Shared", "sourcePath": source}, "definition": map[string]any{"key": "library-owner", "mode": "GENERAL", "modelConfig": map[string]any{"modelKey": "mock-model"}, "runtimeConfig": map[string]any{"workspaceRoot": workspace}}}
	created := postAgentJSON[api.AgentDetailResponse](t, f.server, "/api/admin/agents/create", request)
	id := contracts.AnyStringNode(contracts.AnyMapNode(created.Definition["kbaseConfig"])["libraryId"])
	d, err := center.Get(id)
	if err != nil || d.Collections[0].SourcePath != source {
		t.Fatalf("binding: %+v %v", d, err)
	}
	// A repeated Agent key fails after staging a new library; no orphan remains.
	payload, _ := json.Marshal(request)
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/admin/agents/create", bytes.NewReader(payload)))
	if w.Code == 200 {
		t.Fatal("duplicate Agent accepted")
	}
	libraries, err := center.List()
	if err != nil || len(libraries) != 1 {
		t.Fatalf("creation rollback: %+v %v", libraries, err)
	}
	w = httptest.NewRecorder()
	f.server.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/admin/kbases/"+id, nil))
	if w.Code != 409 || !bytes.Contains(w.Body.Bytes(), []byte("library-owner")) {
		t.Fatalf("reference guard: %d %s", w.Code, w.Body.String())
	}
	postAgentJSON[map[string]any](t, f.server, "/api/admin/agents/delete", map[string]any{"key": created.Key})
	w = httptest.NewRecorder()
	f.server.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/admin/kbases/"+id, nil))
	if w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
}
