package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConnectorManualTokenHTTPRedactionAndValidation(t *testing.T) {
	fixture := setupAdminRegistriesFixture(t)
	root := fixture.server.deps.Config.Paths.EffectiveConnectorsCenterDir()
	writeMCPConnectorForTest(t, root, "demo")
	manifest := `{"id":"demo","name":"Demo","version":"1.0.0","type":"mcp","auth_mode":"token","token_schema":{"fields":[{"key":"API_KEY","required":true,"type":"password"}]}}`
	if err := os.WriteFile(filepath.Join(root, "demo", "connector.json"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
	call := func(method, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(method, "/api/admin/connectors/auth?id=demo", strings.NewReader(body)))
		if strings.Contains(rec.Body.String(), "private-key") {
			t.Fatal("response leaked credentials")
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("credential response is cacheable")
		}
		return rec
	}
	for _, bad := range []string{`{}`, `{"credentials":{"API_KEY":"private-key","API_KEY":"second"}}`, `{"credentials":{"API_KEY":"private-key"},"unknown":true}`, `{"credentials":{"API_KEY":"private-key"}} {}`} {
		if rec := call(http.MethodPut, bad); rec.Code != 400 {
			t.Fatal("invalid credential request accepted", rec.Body.String())
		}
	}
	if rec := call(http.MethodPut, `{"credentials":{"API_KEY":"private-key"}}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"authorized"`) {
		t.Fatal("token save failed", rec.Body.String())
	}
	if rec := call(http.MethodGet, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"authorized"`) {
		t.Fatal("token status failed", rec.Body.String())
	}
	if rec := call(http.MethodDelete, ""); rec.Code != 200 {
		t.Fatal("logout failed", rec.Body.String())
	}
	if rec := call(http.MethodGet, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"unauthorized"`) {
		t.Fatal("logout retained credentials", rec.Body.String())
	}
}
