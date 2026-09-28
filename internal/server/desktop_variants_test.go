package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/connector"
)

func TestDesktopVariantCatalogLocalized(t *testing.T) {
	f := setupAdminRegistriesFixture(t)
	f.server.deps.Config.Paths.BuiltinConnectorsDir = ""
	release, err := f.server.deps.Config.Paths.PrepareNativeConnectors()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, endpoint := range []string{"/api/connectors", "/api/admin/connectors"} {
		for _, tc := range []struct{ locale, full, web string }{{"zh-CN", "桌面端", "桌面端（网页）"}, {"en", "Desktop", "Desktop (Web)"}, {"zh", "桌面端", "桌面端（网页）"}, {"en-US", "Desktop", "Desktop (Web)"}} {
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
				if !connector.IsDesktop(item.ID) {
					continue
				}
				found++
				want, other := tc.full, connector.DesktopWebConnectorID
				if item.ID == connector.DesktopWebConnectorID {
					want, other = tc.web, connector.DesktopConnectorID
				}
				if item.Name != want || item.I18N != nil || !item.Builtin || !item.ReadOnly || item.AuthMode != connector.AuthNoAuth || len(item.NativeTools) != 2 || len(item.Skills) != 2 || len(item.MutuallyExclusiveWith) != 1 || item.MutuallyExclusiveWith[0] != other {
					t.Fatalf("%s: %+v", tc.locale, item)
				}
			}
			if found != 2 {
				t.Fatalf("variants: %d", found)
			}
		}
	}
}

func TestDesktopVariantConflictHTTPPreservesSelection(t *testing.T) {
	f := newTestFixtureWithModelHandlerAndOptions(t, nil, testFixtureOptions{setupRuntime: func(_ string, cfg *config.Config) {
		release, err := cfg.Paths.PrepareNativeConnectors()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(release)
	}})
	agentConnectorResponse(t, agentConnectorRequest(f.server, "PUT", "", map[string]any{"agentKey": "mock-agent", "connectorId": "builtin.desktop-web", "enabled": true}))
	req := httptest.NewRequest(http.MethodPut, "/api/admin/agents/connectors?locale=zh-CN", strings.NewReader(`{"agentKey":"mock-agent","connectorId":"builtin.desktop","enabled":true}`))
	rec := httptest.NewRecorder()
	f.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "同一智能体只能选择") {
		t.Fatalf("conflict: %d %s", rec.Code, rec.Body.String())
	}
	after := agentConnectorResponse(t, agentConnectorRequest(f.server, "GET", "mock-agent", nil))
	for _, id := range after.ConnectorIDs {
		if id == connector.DesktopConnectorID {
			t.Fatal("conflicting choice saved")
		}
	}
}
