package config

import (
	"fmt"
	"strings"
)

// ParseToolNames validates configuration lists without silently dropping typos.
func ParseToolNames(value any, field string) ([]string, error) {
	values, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array of tool names", field)
	}
	names := []string{}
	seen := map[string]bool{}
	for _, raw := range values {
		name, ok := raw.(string)
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, fmt.Errorf("%s must contain non-empty tool names", field)
		}
		if !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
	}
	return names, nil
}
