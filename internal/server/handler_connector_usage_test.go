package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	"agent-platform/internal/connectortest"
	"agent-platform/internal/ws"
)

func connectorUsageFixture(t *testing.T) testFixture {
	t.Helper()
	return newTestFixtureWithModelHandlerAndOptions(t, nil, testFixtureOptions{notifications: ws.NewHub(), setupRuntime: func(root string, cfg *config.Config) {
		cfg.PresetConnectors = []string{connector.WebControlConnectorID, "docs"}
		cfg.ModePresets = map[string]config.AgentPresets{"coder": {Connectors: []string{"mail"}}}
		cfg.Paths.BuiltinConnectorsDir = filepath.Join(root, "builtin-connectors")
		if err := connectortest.WriteCLI(filepath.Join(cfg.Paths.BuiltinConnectorsDir, "builtin.dbx"), "dbx", "1.0.0"); err != nil {
			t.Fatal(err)
		}
		release, err := cfg.Paths.PrepareNativeConnectors()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(release)
		for _, id := range []string{"docs", "meeting", "mail"} {
			writeMCPConnectorForTest(t, cfg.Paths.EffectiveConnectorsCenterDir(), id)
		}
		manifestPath := filepath.Join(cfg.Paths.EffectiveConnectorsCenterDir(), "mail", "connector.json")
		data, _ := os.ReadFile(manifestPath)
		var manifest connector.Manifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			t.Fatal(err)
		}
		manifest.MutuallyExclusiveWith = []string{"docs", "meeting", "not-installed"}
		data, _ = json.Marshal(manifest)
		if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
		data, err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, []byte("\nconnectorConfig:\n  connectors:\n    - builtin.web-control\n    - docs\n    - meeting\n")...)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}})
}

func connectorUsageRequest(s *Server, method, path string, payload any) *httptest.ResponseRecorder {
	var body bytes.Buffer
	if payload != nil {
		_ = json.NewEncoder(&body).Encode(payload)
	}
	req := httptest.NewRequest(method, path, &body)
	req.Header.Set("Accept-Language", "zh-CN")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func TestConnectorUsageCatalogIsScopedAndMinimal(t *testing.T) {
	f := connectorUsageFixture(t)
	for _, tc := range []struct {
		path string
		mail bool
	}{{"/api/connectors", false}, {"/api/connectors?agentKey=mock-agent", true}} {
		rec := connectorUsageRequest(f.server, http.MethodGet, tc.path, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("usage catalog: %d %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Data struct {
				Connectors []map[string]any `json:"connectors"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, item := range body.Data.Connectors {
			id := item["id"].(string)
			ids = append(ids, id)
			for key := range item {
				if !slices.Contains([]string{"id", "name", "description", "iconUrl", "mutuallyExclusiveWith"}, key) {
					t.Fatalf("unexpected usage field %s: %s", key, rec.Body.String())
				}
			}
			if id == "mail" && !reflect.DeepEqual(item["mutuallyExclusiveWith"], []any{"meeting"}) {
				t.Fatalf("hidden/unknown conflict leaked: %+v", item)
			}
			if id == "meeting" {
				for _, key := range []string{"description", "iconUrl", "mutuallyExclusiveWith"} {
					if _, present := item[key]; present {
						t.Fatalf("empty optional field %s: %+v", key, item)
					}
				}
			}
			if id == connector.PlatformControlConnectorID && (item["name"] != "平台控制" || item["iconUrl"] == "") {
				t.Fatalf("localized name/icon lost: %+v", item)
			}
		}
		if slices.Contains(ids, connector.WebControlConnectorID) || slices.Contains(ids, "docs") || slices.Contains(ids, "mail") != tc.mail || !slices.Contains(ids, "builtin.dbx") {
			t.Fatalf("wrong selectable catalog: %v", ids)
		}
	}
	rec := connectorUsageRequest(f.server, http.MethodGet, "/api/connectors?agentKey=missing", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown Agent: %d %s", rec.Code, rec.Body.String())
	}
	rec = connectorUsageRequest(f.server, http.MethodGet, "/api/admin/connectors", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"builtin.web-control"`) || !strings.Contains(rec.Body.String(), `"readOnly":true`) || !strings.Contains(rec.Body.String(), `"canDelete":false`) {
		t.Fatalf("management catalog changed: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAgentConnectorUsageSelectionAndPendingPublication(t *testing.T) {
	f := connectorUsageFixture(t)
	registry := f.registry.(*catalog.FileRegistry)
	registry.SetRuntimeReload(func() {
		if err := f.server.reloadAgentCatalog(context.Background()); err != nil {
			t.Errorf("reload after lease: %v", err)
		}
	})
	read := func(rec *httptest.ResponseRecorder) api.AgentConnectorsResponse {
		t.Helper()
		if rec.Code != http.StatusOK {
			t.Fatalf("usage selection: %d %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Data) != 3 || body.Data["agentKey"] == nil || body.Data["connectorIds"] == nil || body.Data["reloadPending"] == nil {
			t.Fatalf("usage state leaked management fields: %s", rec.Body.String())
		}
		data, _ := json.Marshal(body.Data)
		var state api.AgentConnectorsResponse
		if err := json.Unmarshal(data, &state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	initial := read(connectorUsageRequest(f.server, http.MethodGet, "/api/agents/connectors?agentKey=mock-agent", nil))
	if initial.AgentKey != "mock-agent" || !reflect.DeepEqual(initial.ConnectorIDs, []string{"meeting"}) || initial.ReloadPending {
		t.Fatalf("preset declaration visible: %+v", initial)
	}
	_, release, ok := registry.AcquireAgentRuntime("mock-agent")
	if !ok {
		t.Fatal("missing runtime")
	}
	t.Cleanup(release)
	updated := read(connectorUsageRequest(f.server, http.MethodPut, "/api/agents/connectors", map[string]any{"agentKey": "mock-agent", "connectorId": "meeting", "enabled": false}))
	if len(updated.ConnectorIDs) != 0 || updated.ConnectorIDs == nil || !updated.ReloadPending {
		t.Fatalf("pending publication lost: %+v", updated)
	}
	managed := agentConnectorResponse(t, agentConnectorRequest(f.server, http.MethodGet, "mock-agent", nil))
	if !managed.ReloadPending || !slices.Contains(managed.ActiveConnectorIDs, "meeting") || !slices.Contains(managed.PresetConnectorIDs, connector.WebControlConnectorID) {
		t.Fatalf("management state lost: %+v", managed)
	}
	release()
	final := read(connectorUsageRequest(f.server, http.MethodGet, "/api/agents/connectors?agentKey=mock-agent", nil))
	if final.ReloadPending {
		t.Fatalf("runtime did not publish: %+v", final)
	}
	if def, ok := registry.AgentDefinition("mock-agent"); !ok || !slices.Contains(def.Connectors, connector.WebControlConnectorID) || !slices.Contains(def.Tools, "surface_list") {
		t.Fatal("hiding preset removed runtime capability")
	}
}

func TestAgentConnectorEndpointsRejectPresetChanges(t *testing.T) {
	f := connectorUsageFixture(t)
	registry := f.registry.(*catalog.FileRegistry)
	before, _ := registry.ReadEditableAgentSource("mock-agent")
	for _, path := range []string{"/api/agents/connectors", "/api/admin/agents/connectors"} {
		for _, enabled := range []bool{true, false} {
			rec := connectorUsageRequest(f.server, http.MethodPut, path, map[string]any{"agentKey": "mock-agent", "connectorId": connector.WebControlConnectorID, "enabled": enabled})
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "preset_connector_readonly") {
				t.Fatalf("preset toggle accepted: %d %s", rec.Code, rec.Body.String())
			}
		}
	}
	for _, payload := range []map[string]any{
		{"agentKey": "mock-agent", "connectorId": "meeting"},
		{"agentKey": "mock-agent", "connectorId": "meeting", "enabled": true, "presetConnectorIds": []string{}},
		{"agentKey": "missing", "connectorId": "meeting", "enabled": true},
	} {
		rec := connectorUsageRequest(f.server, http.MethodPut, "/api/agents/connectors", payload)
		if rec.Code < 400 || rec.Code >= 500 {
			t.Fatalf("invalid edit accepted: %d %s", rec.Code, rec.Body.String())
		}
	}
	if rec := connectorUsageRequest(f.server, http.MethodGet, "/api/agents/connectors", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing Agent accepted: %d", rec.Code)
	}
	after, _ := registry.ReadEditableAgentSource("mock-agent")
	if before.Content != after.Content {
		t.Fatal("rejected edits changed source")
	}
}
