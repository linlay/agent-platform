package server

import (
	"net"
	"net/http"
	"net/url"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/webapphost"
)

// Classify before CORS, platform authentication and the API mux. Future app
// hosts must never fall through to platform APIs, including /healthz or /ws.
func (s *Server) handleWebAppHostBoundary(w http.ResponseWriter, r *http.Request) bool {
	u, _ := url.Parse(s.deps.Config.WebApps.PlatformOrigin)
	if u == nil || !strings.EqualFold(r.Host, u.Host) {
		peer, _, _ := net.SplitHostPort(r.RemoteAddr)
		host, port, _ := net.SplitHostPort(r.Host)
		if r.Method == http.MethodGet && r.URL.Path == "/healthz" &&
			port == s.deps.Config.Server.Port && net.ParseIP(host).IsLoopback() && net.ParseIP(peer).IsLoopback() {
			return false
		}
		http.NotFound(w, r)
		return true
	}
	if raw := r.Header.Get("Origin"); raw != "" {
		origin, err := url.Parse(raw)
		if err == nil {
			host := strings.ToLower(origin.Hostname())
			domain := s.deps.Config.WebApps.ContentDomain
			if domain != "" && (host == domain || strings.HasSuffix(host, "."+domain)) || (host == "127.0.0.1" && raw != s.deps.Config.WebApps.PlatformOrigin) {
				writeJSON(w, http.StatusForbidden, api.Failure(403, "webapp_origin_not_allowed"))
				return true
			}
		}
	}
	return false
}

func (s *Server) handleWebAppHost(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p := PrincipalFromContext(r.Context())
	owners := s.deps.Config.Auth.AllowedSubjects
	if p == nil || p.Subject == "" || len(owners) != 1 || p.Subject != owners[0] {
		writeJSON(w, http.StatusForbidden, api.Failure(403, "webapp_owner_required"))
		return
	}
	if s.deps.WebAppHost == nil {
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(503, "webapp_host_unavailable"))
		return
	}
	// Report the assembled host capabilities and actual isolation prerequisites.
	// Static serving can remain available when Node prerequisites are missing.
	status := *s.deps.WebAppHost
	if s.deps.WebApps != nil {
		status.Stage = "installation"
		status.MaxArchiveMiB = s.deps.Config.WebApps.MaxArchiveMiB
	}
	if s.webAppGateway != nil {
		status.Stage = "static"
		status.StaticAvailable = true
	}
	if s.deps.WebApps != nil && s.deps.WebApps.BackendAvailable() {
		status.Stage = "backend"
		status.HostingAvailable = true
	}
	status.Issues = append([]webapphost.Issue{}, status.Issues...)
	if s.deps.WebApps != nil {
		for _, code := range s.deps.WebApps.Issues() {
			status.Issues = append(status.Issues, webapphost.Issue{Code: code})
		}
	}
	status.Warnings = append([]webapphost.Issue{}, status.Warnings...)
	writeJSON(w, http.StatusOK, api.Success(status))
}
