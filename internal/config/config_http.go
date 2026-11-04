package config

import (
	"fmt"
	"strings"
	"time"
)

func (c *Config) applyHTTPProxy(value any) error {
	values, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("http-proxy must be a mapping")
	}
	for key, value := range values {
		switch key {
		case "mode", "url":
			s, ok := value.(string)
			if !ok {
				return fmt.Errorf("http-proxy %s must be a string", key)
			}
			if key == "mode" {
				c.HTTPProxy.Mode = strings.TrimSpace(s)
			} else {
				c.HTTPProxy.URL = strings.TrimSpace(s)
			}
		case "bypass":
			// The runtime YAML subset leaves nonempty flow lists as strings.
			if raw, ok := value.(string); ok && strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
				entries, err := splitYAMLFlowEntries(raw[1 : len(raw)-1])
				if err != nil {
					return fmt.Errorf("http-proxy bypass must be a string list")
				}
				list := make([]any, 0, len(entries))
				for _, entry := range entries {
					list = append(list, normalizeYAMLTreeScalars(parseYAMLScalar(entry), YAMLTreeOptions{}))
				}
				value = list
			}
			list, ok := value.([]any)
			if !ok {
				return fmt.Errorf("http-proxy bypass must be a string list")
			}
			c.HTTPProxy.Bypass = nil
			for _, item := range list {
				s, ok := item.(string)
				if !ok {
					return fmt.Errorf("http-proxy bypass must be a string list")
				}
				c.HTTPProxy.Bypass = append(c.HTTPProxy.Bypass, strings.TrimSpace(s))
			}
		case "system-refresh-interval":
			s, ok := value.(string)
			if !ok {
				return fmt.Errorf("http-proxy system-refresh-interval must be a duration")
			}
			d, err := time.ParseDuration(s)
			if err != nil || d <= 0 {
				return fmt.Errorf("http-proxy system-refresh-interval must be a positive duration")
			}
			c.HTTPProxy.SystemRefreshInterval = d
		default:
			return fmt.Errorf("unknown http-proxy field %q", key)
		}
	}
	return c.HTTPProxy.Validate()
}
