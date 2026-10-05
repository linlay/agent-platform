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

func TestDesktopACPRegistrationAuthority(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coder-settings.yml")
	s := &Server{router: http.NewServeMux(), acpRegistrations: config.NewACPRegistrationStore(config.CoderSettingsConfig{SourcePath: path})}
	s.routes()
	body := `{"sourcePluginId":"codex-plugin","bridgeId":"codex","baseUrl":"http://127.0.0.1:17071"}`
	for _, p := range []*Principal{nil, {Subject: "user", Claims: map[string]any{"scope": "web", "deviceId": "device"}}, {Subject: "user", Claims: map[string]any{"scope": "app"}}} {
		req := httptest.NewRequest(http.MethodPut, "/api/desktop/acp-bridges", strings.NewReader(body))
		if p != nil {
			req = req.WithContext(WithPrincipal(req.Context(), p))
		}
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, req)
		if w.Code != 403 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("denied caller wrote config")
	}
	p := &Principal{Subject: "user", Claims: map[string]any{"scope": "app", "deviceId": "device"}}
	for _, tc := range []struct {
		method, body string
		status       int
	}{
		{http.MethodPut, body, 200},
		{http.MethodPut, strings.ReplaceAll(body, "codex-plugin", "other"), 409},
		{http.MethodPut, strings.TrimSuffix(body, "}") + `,"path":"/tmp/elsewhere"}`, 400},
		{http.MethodGet, body, 405},
		{http.MethodDelete, `{"sourcePluginId":"codex-plugin","bridgeId":"codex"}`, 200},
	} {
		req := httptest.NewRequest(tc.method, "/api/desktop/acp-bridges", strings.NewReader(tc.body))
		req = req.WithContext(WithPrincipal(req.Context(), p))
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatal(tc.method, tc.body, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "127.0.0.1") || strings.Contains(w.Body.String(), path) {
			t.Fatal("response disclosed configuration")
		}
	}
	// Desktop routes force actual token validation even with auth.enabled=false.
	req := httptest.NewRequest(http.MethodPut, "/api/desktop/acp-bridges", strings.NewReader(body))
	w := httptest.NewRecorder()
	if s.withPrincipal(req, w) != nil || w.Code != 401 {
		t.Fatal("unauthenticated route bypass")
	}
}
