package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/webapphost"
)

func webAppBoundaryServer() *Server {
	return &Server{deps: Dependencies{Config: config.Config{
		Server:  config.ServerConfig{Port: "8080"},
		WebApps: config.WebAppsConfig{Enabled: true, PlatformOrigin: "https://demo.platform.example.com", ContentDomain: "apps.example.net"},
		Auth:    config.AuthConfig{Enabled: true, AllowedSubjects: []string{"owner"}},
	}, WebAppHost: &webapphost.Status{Host: "compose", Stage: "foundation", Issues: []webapphost.Issue{}}}}
}

func TestWebAppHostCannotFallThroughToPlatform(t *testing.T) {
	s := webAppBoundaryServer()
	for _, tc := range []struct {
		host, path, peer, forwarded string
		blocked                     bool
	}{
		{"demo.platform.example.com", "/api/admin/agents", "192.0.2.1:1000", "", false},
		{"demo--0123456789abcdef.apps.example.net", "/api/admin/agents", "192.0.2.1:1000", "demo.platform.example.com", true},
		{"unknown.example.com", "/healthz", "192.0.2.1:1000", "", true},
		{"127.0.0.1:8080", "/healthz", "127.0.0.1:50000", "", false},
		{"127.0.0.1:8080", "/healthz", "192.0.2.1:50000", "", true},
		{"127.0.0.1:8080", "/api/admin/agents", "127.0.0.1:50000", "", true},
		{"127.0.0.1:8081", "/healthz", "127.0.0.1:50000", "", true},
	} {
		r := httptest.NewRequest(http.MethodGet, "http://"+tc.host+tc.path, nil)
		r.RemoteAddr = tc.peer
		r.Header.Set("X-Forwarded-Host", tc.forwarded)
		w := httptest.NewRecorder()
		if got := s.handleWebAppHostBoundary(w, r); got != tc.blocked {
			t.Fatalf("%s%s blocked=%t want=%t", tc.host, tc.path, got, tc.blocked)
		}
	}
}

func TestWebAppOriginsCannotReachPlatformWSOrAPI(t *testing.T) {
	s := webAppBoundaryServer()
	for _, path := range []string{"/ws", "/api/admin/webapps/host"} {
		r := httptest.NewRequest(http.MethodGet, "https://demo.platform.example.com"+path, nil)
		r.Header.Set("Origin", "https://demo--0123456789abcdef.apps.example.net")
		w := httptest.NewRecorder()
		if !s.handleWebAppHostBoundary(w, r) || w.Code != http.StatusForbidden {
			t.Fatalf("cross-application origin accepted: %s", path)
		}
	}
}

func TestWebAppHostStatusRequiresOwnerAndDoesNotClaimHosting(t *testing.T) {
	s := webAppBoundaryServer()
	for _, subject := range []string{"", "someone-else", "owner"} {
		r := httptest.NewRequest(http.MethodGet, "https://demo.platform.example.com/api/admin/webapps/host", nil)
		r = r.WithContext(WithPrincipal(r.Context(), &Principal{Subject: subject}))
		w := httptest.NewRecorder()
		s.handleWebAppHost(w, r)
		if subject != "owner" {
			if w.Code != http.StatusForbidden {
				t.Fatal("non owner read host status")
			}
			continue
		}
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"hostingAvailable":false`) || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	}
}
