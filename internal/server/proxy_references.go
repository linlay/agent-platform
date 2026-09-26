package server

import (
	"net/http"
	"strings"

	proxy "agent-platform/internal/runtime/proxy"
)

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

type proxyReferenceOptions = proxy.ReferenceOptions

var prepareProxyReferences = proxy.PrepareReferences

var sha256FileHex = proxy.Sha256FileHex
var pathWithinBase = proxy.PathWithinBase
