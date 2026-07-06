package server

import (
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"agent-platform/internal/observability"
)

func (s *Server) handleCORS(w http.ResponseWriter, r *http.Request) bool {
	cfg := s.deps.Config.CORS
	if !cfg.Enabled || !strings.HasPrefix(r.URL.Path, "/api/") {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin != "" && originAllowed(origin, cfg.AllowedOriginPatterns) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
	}
	if cfg.AllowCredentials {
		w.Header().Set("Access-Control-Allow-Credentials", "true")
	}
	if len(cfg.ExposedHeaders) > 0 {
		w.Header().Set("Access-Control-Expose-Headers", strings.Join(cfg.ExposedHeaders, ", "))
	}
	if r.Method != http.MethodOptions {
		return false
	}
	if len(cfg.AllowedMethods) > 0 {
		w.Header().Set("Access-Control-Allow-Methods", strings.Join(cfg.AllowedMethods, ", "))
	}
	if len(cfg.AllowedHeaders) > 0 {
		w.Header().Set("Access-Control-Allow-Headers", strings.Join(cfg.AllowedHeaders, ", "))
	}
	if cfg.MaxAgeSeconds > 0 {
		w.Header().Set("Access-Control-Max-Age", fmt.Sprintf("%d", cfg.MaxAgeSeconds))
	}
	w.WriteHeader(http.StatusOK)
	return true
}

func (s *Server) withPrincipal(r *http.Request, w http.ResponseWriter) *http.Request {
	if !s.deps.Config.Auth.Enabled || !strings.HasPrefix(r.URL.Path, "/api/") {
		return r
	}
	if r.Method == http.MethodOptions {
		return r
	}
	if isPublicMonitorRequest(r) {
		return r
	}
	if r.Method == http.MethodGet && (r.URL.Path == "/api/resource" || r.URL.Path == "/api/tool-result") {
		if !s.deps.Config.ResourceTicket.Enabled() {
			return r
		}
		if strings.TrimSpace(r.URL.Query().Get("t")) != "" {
			return r
		}
	}
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(authorization, "Bearer ") {
		writeAuthError(w)
		return nil
	}
	principal, err := s.authVerifier.Verify(strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer ")))
	if err != nil {
		writeAuthError(w)
		return nil
	}
	return r.WithContext(WithPrincipal(r.Context(), principal))
}

func isPublicMonitorRequest(r *http.Request) bool {
	if r == nil || r.Method != http.MethodGet {
		return false
	}
	switch r.URL.Path {
	case "/api/monitor", "/api/monitor/channels", "/api/monitor/ws/connections", "/api/monitor/ws/messages":
		return true
	default:
		return false
	}
}

func writeAuthError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
}

func (s *Server) logRequest(r *http.Request, status int, cost time.Duration) {
	if !s.deps.Config.Logging.Request.Enabled {
		return
	}
	observability.LogRequest(r, status, cost)
	log.Printf("%s %s -> %d (%s)", r.Method, observability.SanitizeLog(r.URL.RequestURI()), status, cost.Round(time.Millisecond))
}

func originAllowed(origin string, allowed []string) bool {
	if len(allowed) == 0 {
		return false
	}
	for _, pattern := range allowed {
		if pattern == "*" || strings.EqualFold(strings.TrimSpace(pattern), origin) {
			return true
		}
	}
	return false
}

func resourceBelongsToChat(fileParam string, chatID string) bool {
	clean := filepath.ToSlash(filepath.Clean(fileParam))
	return clean == chatID || strings.HasPrefix(clean, chatID+"/")
}
