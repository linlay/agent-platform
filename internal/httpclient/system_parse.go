package httpclient

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Parse only the global dictionary; interface-specific __SCOPED__ settings
// cannot be selected correctly without knowing the request's network route.
func parseDarwinSettings(output string) (systemSettings, error) {
	s := systemSettings{}
	values := map[string]string{}
	depth, exceptionsDepth := 0, 0
	rootSeen := false
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasSuffix(line, "{") {
			if depth == 0 {
				if line != "<dictionary> {" || rootSeen {
					return s, errors.New("invalid scutil proxy dictionary")
				}
				rootSeen = true
			}
			if depth == 1 && strings.HasPrefix(line, "ExceptionsList : <array>") {
				exceptionsDepth = 2
			}
			depth++
			continue
		}
		if line == "}" {
			if depth == exceptionsDepth {
				exceptionsDepth = 0
			}
			depth--
			if depth < 0 {
				return s, errors.New("invalid scutil proxy dictionary")
			}
			continue
		}
		key, value, ok := strings.Cut(line, " : ")
		if !ok {
			return s, errors.New("invalid scutil proxy value")
		}
		if depth == 1 {
			values[key] = value
		}
		if exceptionsDepth != 0 && depth == exceptionsDepth {
			s.Bypass = append(s.Bypass, value)
		}
	}
	if scanner.Err() != nil || !rootSeen || depth != 0 {
		return s, errors.New("incomplete scutil proxy dictionary")
	}
	for _, entry := range []struct{ name, scheme string }{{"HTTP", "http"}, {"HTTPS", "http"}, {"SOCKS", "socks5"}} {
		enabled := values[entry.name+"Enable"]
		if enabled != "1" {
			if enabled != "" && enabled != "0" {
				return s, errors.New("invalid system proxy enable flag")
			}
			continue
		}
		host := strings.Trim(values[entry.name+"Proxy"], "[]")
		port := values[entry.name+"Port"]
		n, err := strconv.Atoi(port)
		if host == "" || strings.ContainsAny(host, " /@?#\\") || err != nil || n < 1 || n > 65535 {
			return s, fmt.Errorf("invalid system %s proxy endpoint", entry.name)
		}
		p, err := parseProxy(entry.scheme + "://" + net.JoinHostPort(host, port))
		if err != nil {
			return s, err
		}
		switch entry.name {
		case "HTTP":
			s.HTTP = p
		case "HTTPS":
			s.HTTPS = p
		case "SOCKS":
			s.SOCKS = p
		}
	}
	s.ExcludeSimple = values["ExcludeSimpleHostnames"] == "1"
	s.Auto = values["ProxyAutoConfigEnable"] == "1" || values["ProxyAutoDiscoveryEnable"] == "1"
	return s, nil
}

func parseWindowsSettings(proxy, bypass string, auto bool) (systemSettings, error) {
	s := systemSettings{Auto: auto, Bypass: strings.FieldsFunc(bypass, func(c rune) bool { return c == ';' || c == ' ' || c == '\t' })}
	for _, entry := range strings.FieldsFunc(proxy, func(c rune) bool { return c == ';' || c == ' ' || c == '\t' }) {
		kind, address, perScheme := strings.Cut(entry, "=")
		if !perScheme {
			kind, address = "", entry
		}
		kind = strings.ToLower(kind)
		if kind != "" && kind != "http" && kind != "https" && kind != "socks" {
			continue
		}
		if kind == "socks" && !strings.Contains(address, "://") {
			address = "socks5://" + address
		}
		p, err := parseProxy(address)
		if err != nil {
			return s, fmt.Errorf("invalid Windows fixed proxy: %w", err)
		}
		switch kind {
		case "":
			s.HTTP = p
			s.HTTPS = p
		case "http":
			s.HTTP = p
		case "https":
			s.HTTPS = p
		case "socks":
			s.SOCKS = p
		}
	}
	return s, nil
}
