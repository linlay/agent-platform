package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/connector"
)

func TestNativeConnectorCatalogLocalized(t *testing.T) {
	f := setupAdminRegistriesFixture(t)
	f.server.deps.Config.Paths.BuiltinConnectorsDir = ""
	release, err := f.server.deps.Config.Paths.PrepareNativeConnectors()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, endpoint := range []string{"/api/connectors", "/api/admin/connectors"} {
		for _, tc := range []struct{ locale, desktop, web string }{{"zh-CN", "平台控制", "网页控制"}, {"en", "Platform Control", "Web Control"}, {"zh", "平台控制", "网页控制"}, {"en-US", "Platform Control", "Web Control"}} {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, endpoint, nil)
			req.Header.Set("Accept-Language", tc.locale)
			f.server.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("catalog: %d %s", rec.Code, rec.Body.String())
			}
			var body struct {
				Data struct {
					Connectors []connector.Summary `json:"connectors"`
				} `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			found := 0
			for _, item := range body.Data.Connectors {
				if !connector.IsNative(item.ID) {
					continue
				}
				found++
				want, tools := tc.desktop, 14
				if item.ID == connector.WebControlConnectorID {
					want, tools = tc.web, 15
				}
				if item.Name != want || item.I18N != nil || !item.Builtin || !item.ReadOnly || item.AuthMode != connector.AuthNoAuth || len(item.NativeTools) != tools || len(item.Skills) != 1 || len(item.MutuallyExclusiveWith) != 0 {
					t.Fatalf("%s: %+v", tc.locale, item)
				}
			}
			if found != 2 {
				t.Fatalf("native connectors: %d", found)
			}
		}
	}
}

func TestNativeConnectorsCanBeSelectedTogether(t *testing.T) {
	f := newTestFixtureWithModelHandlerAndOptions(t, nil, testFixtureOptions{setupRuntime: func(_ string, cfg *config.Config) {
		release, err := cfg.Paths.PrepareNativeConnectors()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(release)
	}})
	agentConnectorResponse(t, agentConnectorRequest(f.server, "PUT", "", map[string]any{"agentKey": "mock-agent", "connectorId": connector.WebControlConnectorID, "enabled": true}))
	agentConnectorResponse(t, agentConnectorRequest(f.server, "PUT", "", map[string]any{"agentKey": "mock-agent", "connectorId": connector.PlatformControlConnectorID, "enabled": true}))
	after := agentConnectorResponse(t, agentConnectorRequest(f.server, "GET", "mock-agent", nil))
	if !reflect.DeepEqual(after.ConnectorIDs, []string{connector.WebControlConnectorID, connector.PlatformControlConnectorID}) {
		t.Fatalf("selection: %v", after.ConnectorIDs)
	}
	// The retired web variant is not a selectable connector.
	rec := agentConnectorRequest(f.server, "PUT", "", map[string]any{"agentKey": "mock-agent", "connectorId": "builtin.desktop-web", "enabled": true})
	if rec.Code == http.StatusOK {
		t.Fatalf("retired connector accepted: %s", rec.Body.String())
	}
}

func TestExternalConnectorSelectionConflictUsesManifest(t *testing.T) {
	f := newTestFixtureWithModelHandlerAndOptions(t, nil, testFixtureOptions{setupRuntime: func(_ string, cfg *config.Config) {
		for _, id := range []string{"custom-first", "custom-second"} {
			writeMCPConnectorForTest(t, cfg.Paths.EffectiveConnectorsCenterDir(), id)
		}
		file := filepath.Join(cfg.Paths.EffectiveConnectorsCenterDir(), "custom-first", "connector.json")
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var manifest connector.Manifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			t.Fatal(err)
		}
		manifest.MutuallyExclusiveWith = []string{"custom-second"}
		data, err = json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
	}})
	agentConnectorResponse(t, agentConnectorRequest(f.server, "PUT", "", map[string]any{"agentKey": "mock-agent", "connectorId": "custom-first", "enabled": true}))
	rec := agentConnectorRequest(f.server, "PUT", "", map[string]any{"agentKey": "mock-agent", "connectorId": "custom-second", "enabled": true})
	var body struct {
		Data struct {
			Error struct {
				Code        string   `json:"code"`
				ConnectorID string   `json:"connectorId"`
				Conflicts   []string `json:"conflictingConnectorIds"`
			} `json:"error"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 400 || body.Data.Error.Code != "connector_selection_conflict" || body.Data.Error.ConnectorID != "custom-second" || !reflect.DeepEqual(body.Data.Error.Conflicts, []string{"custom-first"}) {
		t.Fatalf("conflict: %d %s", rec.Code, rec.Body.String())
	}
	after := agentConnectorResponse(t, agentConnectorRequest(f.server, "GET", "mock-agent", nil))
	if !reflect.DeepEqual(after.ConnectorIDs, []string{"custom-first"}) {
		t.Fatalf("selection changed: %v", after.ConnectorIDs)
	}
	// Removing the existing choice remains possible; the other variant can then be selected.
	agentConnectorResponse(t, agentConnectorRequest(f.server, "PUT", "", map[string]any{"agentKey": "mock-agent", "connectorId": "custom-first", "enabled": false}))
	agentConnectorResponse(t, agentConnectorRequest(f.server, "PUT", "", map[string]any{"agentKey": "mock-agent", "connectorId": "custom-second", "enabled": true}))
}
