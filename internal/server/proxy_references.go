package server

import (
	"net/http"
	"strings"

	proxy "agent-platform/internal/runtime/proxy"
)

// requestBaseURL prefers the configured public origin: request headers are
// client-controlled unless a trusted proxy rewrites them.
func (s *Server) requestBaseURL(r *http.Request) string {
	if public := s.deps.Config.Server.PublicBaseURL; public != "" {
		return public
	}
	return requestBaseURL(r)
}

func requestBaseURL(r *http.Request) string {
	if r == nil {
		return ""
	}
	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = strings.TrimSpace(r.Host)
	}
	if host == "" {
		return ""
	}
	proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
	if proto == "" {
		if r.TLS != nil {
			proto = "https"
		} else {
			proto = "http"
		}
	}
	return strings.TrimRight(proto+"://"+host, "/")
}

var sha256FileHex = proxy.Sha256FileHex
var pathWithinBase = proxy.PathWithinBase
