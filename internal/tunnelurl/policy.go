package tunnelurl

import (
	"net"
	"strings"
)

// IsLoopbackHostname reports whether hostname is one of the Tunnel protocol's
// three canonical local hosts. Callers must pass a parsed URL hostname.
func IsLoopbackHostname(hostname string) bool {
	return strings.EqualFold(hostname, "localhost") || hostname == "127.0.0.1" || hostname == "::1"
}

// IsForbiddenHostname rejects reserved local aliases that must not be treated
// as either canonical loopback hosts or remote Tunnel endpoints.
func IsForbiddenHostname(hostname string) bool {
	normalized := strings.ToLower(hostname)
	if IsLoopbackHostname(normalized) {
		return false
	}
	if normalized == "0.0.0.0" || strings.HasSuffix(normalized, ".localhost") {
		return true
	}
	ipv4 := net.ParseIP(normalized).To4()
	return ipv4 != nil && ipv4[0] == 127
}
