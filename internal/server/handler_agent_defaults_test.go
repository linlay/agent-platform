package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"agent-platform/internal/api"
)

func TestAgentDefaultsExposeRuntimeFacts(t *testing.T) {
	fixture := newTestFixture(t)
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/agents/creation-defaults", nil))
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	var response api.ApiResponse[api.AgentCreationDefaultsResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data.Types) != 4 {
		t.Fatalf("types: %#v", response.Data.Types)
	}
	for _, typ := range response.Data.Types {
		if typ.Engine == "native" && len(typ.BaseTools) == 0 {
			t.Fatalf("missing tools: %#v", typ)
		}
		if typ.Engine == "acp" && len(typ.BaseTools) != 0 {
			t.Fatal("external engine has native tools")
		}
	}
	var wire map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &wire)
	data := wire["data"].(map[string]any)
	if _, ok := data["groups"]; ok {
		t.Fatal("runtime response contains client groups")
	}
}

func TestCreateConcreteDefinitionPersistsClientSelection(t *testing.T) {
	fixture := newTestFixture(t)
	definition := map[string]any{
		"name": "Client selection", "mode": "GENERAL",
		"modelConfig":   map[string]any{"modelKey": "mock-model"},
		"runtimeConfig": map[string]any{"workspaceRoot": t.TempDir()},
		"toolConfig":    map[string]any{"tools": []string{"datetime", "bash"}},
		"skillConfig":   map[string]any{"skills": []string{"mock-skill"}},
	}
	created := postAgentJSON[api.AgentDetailResponse](t, fixture.server, "/api/admin/agents/create", map[string]any{"definition": definition})
	if created.Source == nil {
		t.Fatal("missing source")
	}
	content, err := os.ReadFile(created.Source.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "mock-skill") || !strings.Contains(string(content), "datetime") {
		t.Fatalf("missing client selection: %s", content)
	}
	if strings.Contains(string(content), "web_fetch") {
		t.Fatalf("unrequested tool added: %s", content)
	}
}

func TestAgentCreateRejectsUnknownEnvelopeFields(t *testing.T) {
	fixture := newTestFixture(t)
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/agents/create", bytes.NewBufferString(`{"definition":{"mode":"GENERAL"},"unknownField":[]}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateRejectsUnavailableConcreteResourcesBeforeWriting(t *testing.T) {
	for _, tc := range []struct{ section, field, name string }{
		{"skillConfig", "skills", "missing-skill"},
		{"toolConfig", "tools", "missing_tool"},
		{"connectorConfig", "connectors", "missing.connector"},
	} {
		t.Run(tc.section, func(t *testing.T) {
			fixture := newTestFixture(t)
			definition := map[string]any{"key": "invalid-selection", "name": "Invalid", "mode": "GENERAL", "modelConfig": map[string]any{"modelKey": "mock-model"}, tc.section: map[string]any{tc.field: []string{tc.name}}}
			body, _ := json.Marshal(map[string]any{"key": "invalid-selection", "definition": definition})
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/agents/create", bytes.NewReader(body)))
			if rec.Code == http.StatusOK {
				t.Fatalf("accepted unavailable resource: %s", rec.Body.String())
			}
			if _, found := fixture.server.deps.Registry.AgentDefinition("invalid-selection"); found {
				t.Fatal("invalid agent published")
			}
		})
	}
}

func TestCreateRejectsUnavailableConcreteModel(t *testing.T) {
	fixture := newTestFixture(t)
	body, _ := json.Marshal(map[string]any{"definition": map[string]any{
		"mode": "GENERAL", "modelConfig": map[string]any{"modelKey": "missing-model"},
		"runtimeConfig": map[string]any{"workspaceRoot": t.TempDir()},
	}})
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/agents/create", bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "model_unavailable") {
		t.Fatalf("unexpected response: %d %s", rec.Code, rec.Body.String())
	}
}
