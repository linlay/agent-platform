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
			if host == domain || strings.HasSuffix(host, "."+domain) {
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
	// This endpoint is a prerequisite report. HostingAvailable remains false
	// until the installer, gateway and lifecycle are implemented and verified.
	status := *s.deps.WebAppHost
	status.Issues = append([]webapphost.Issue{}, status.Issues...)
	writeJSON(w, http.StatusOK, api.Success(status))
}
