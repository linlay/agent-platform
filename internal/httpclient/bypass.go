package httpclient

import (
	"net"
	"net/url"
	"path"
	"strings"
)

// System exceptions support exact hosts/IPs, shell wildcards, CIDR, optional
// scheme/port, and Windows <local>/macOS ExcludeSimpleHostnames. No DNS lookup
// is performed (a CIDR rule applies to literal IP destinations only).
func systemBypass(u *url.URL, rules []string, excludeSimple bool) bool {
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	ip := net.ParseIP(host)
	simple := ip == nil && !strings.Contains(host, ".")
	if excludeSimple && simple {
		return true
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	for _, rule := range rules {
		rule = strings.ToLower(strings.TrimSpace(rule))
		if rule == "" {
			continue
		}
		if rule == "<local>" {
			if simple {
				return true
			}
			continue
		}
		if scheme, rest, ok := strings.Cut(rule, "://"); ok {
			if scheme != u.Scheme {
				continue
			}
			rule = rest
		}
		if network := systemCIDR(rule); network != nil {
			if ip != nil && network.Contains(ip) {
				return true
			}
			continue
		}
		if h, p, err := net.SplitHostPort(rule); err == nil {
			if p != port {
				continue
			}
			rule = h
		} else if strings.Count(rule, ":") == 1 {
			h, p, _ := strings.Cut(rule, ":")
			if p != port {
				continue
			}
			rule = h
		}
		rule = strings.TrimSuffix(strings.Trim(rule, "[]"), ".")
		if other := net.ParseIP(rule); other != nil {
			if ip != nil && ip.Equal(other) {
				return true
			}
			continue
		}
		if rule == host {
			return true
		}
		if strings.HasPrefix(rule, ".") && strings.HasSuffix(host, rule) {
			return true
		}
		if ok, _ := path.Match(rule, host); ok {
			return true
		}
	}
	return false
}

func systemCIDR(rule string) *net.IPNet {
	// macOS commonly emits abbreviated IPv4 networks such as 169.254/16.
	if address, bits, ok := strings.Cut(rule, "/"); ok && !strings.Contains(address, ":") {
		parts := strings.Split(address, ".")
		if len(parts) < 4 {
			for len(parts) < 4 {
				parts = append(parts, "0")
			}
			rule = strings.Join(parts, ".") + "/" + bits
		}
	}
	_, network, _ := net.ParseCIDR(rule)
	return network
}
