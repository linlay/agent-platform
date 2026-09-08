package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/config"
)

func TestConnectorOneIDHTTPUsesConfiguredDesktopIdentity(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sso-access-token.txt")
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{}`)) }, testFixtureOptions{
		configure: func(cfg *config.Config) { cfg.IdentityFile = file },
	})
	root := fixture.server.deps.Config.Paths.EffectiveConnectorsCenterDir()
	writeMCPConnectorForTest(t, root, "demo")
	raw := `{"id":"demo","name":"Demo","version":"1.0.0","type":"mcp","auth_mode":"oneid-token"}`
	os.WriteFile(filepath.Join(root, "demo", "connector.json"), []byte(raw), 0644)
	check := func(want string) {
		t.Helper()
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/connectors/auth?id=demo", nil))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"`+want+`"`) {
			t.Fatal(rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "test-private-sso") || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("SSO status leaked credentials or allowed caching")
		}
	}
	check("unauthorized")
	os.WriteFile(file, []byte("test-private-sso"), 0600)
	check("authorized")
	for _, method := range []string{http.MethodPut, http.MethodPost, http.MethodDelete} {
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(method, "/api/admin/connectors/auth?id=demo", strings.NewReader(`{"credentials":{"TOKEN":"manual"}}`)))
		if rec.Code != 400 {
			t.Fatal("SSO accepted connector credential mutation", method, rec.Body.String())
		}
	}
	check("authorized")
	os.Remove(file)
	check("unauthorized")
}
