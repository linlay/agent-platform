package config

import (
	"fmt"
	"strings"
	"time"

	"agent-platform/internal/httpclient"
)

func (c *Config) applyHTTPProxy(value any) error {
	next, err := ParseHTTPProxy(value, c.HTTPProxy)
	if err == nil {
		c.HTTPProxy = next
	}
	return err
}

// ParseHTTPProxy shares the existing proxy configuration without loading Runtime.
func ParseHTTPProxy(value any, next httpclient.Config) (httpclient.Config, error) {
	values, ok := value.(map[string]any)
	if !ok {
		return next, fmt.Errorf("http-proxy must be a mapping")
	}
	for key, value := range values {
		switch key {
		case "mode", "url":
			s, ok := value.(string)
			if !ok {
				return next, fmt.Errorf("http-proxy %s must be a string", key)
			}
			if key == "mode" {
				next.Mode = strings.TrimSpace(s)
			} else {
				next.URL = strings.TrimSpace(s)
			}
		case "bypass":
			// The runtime YAML subset leaves nonempty flow lists as strings.
			if raw, ok := value.(string); ok && strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
				entries, err := splitYAMLFlowEntries(raw[1 : len(raw)-1])
				if err != nil {
					return next, fmt.Errorf("http-proxy bypass must be a string list")
				}
				list := make([]any, 0, len(entries))
				for _, entry := range entries {
					list = append(list, normalizeYAMLTreeScalars(parseYAMLScalar(entry), YAMLTreeOptions{}))
				}
				value = list
			}
			list, ok := value.([]any)
			if !ok {
				return next, fmt.Errorf("http-proxy bypass must be a string list")
			}
			next.Bypass = nil
			for _, item := range list {
				s, ok := item.(string)
				if !ok {
					return next, fmt.Errorf("http-proxy bypass must be a string list")
				}
				next.Bypass = append(next.Bypass, strings.TrimSpace(s))
			}
		case "system-refresh-interval":
			s, ok := value.(string)
			if !ok {
				return next, fmt.Errorf("http-proxy system-refresh-interval must be a duration")
			}
			d, err := time.ParseDuration(s)
			if err != nil || d <= 0 {
				return next, fmt.Errorf("http-proxy system-refresh-interval must be a positive duration")
			}
			next.SystemRefreshInterval = d
		default:
			return next, fmt.Errorf("unknown http-proxy field %q", key)
		}
	}
	return next, next.Validate()
}
