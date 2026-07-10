package terminal

import (
	"runtime"
	"strings"
)

// mergeEnvironment applies overrides without relying on platform-specific
// duplicate-environment semantics. Later entries take precedence.
func mergeEnvironment(base []string, overrides []string) []string {
	merged := append([]string(nil), base...)
	positions := make(map[string]int, len(merged)+len(overrides))
	for index, item := range merged {
		if key, _, ok := strings.Cut(item, "="); ok && key != "" {
			positions[normalizeEnvironmentKey(key)] = index
		}
	}
	for _, item := range overrides {
		key, _, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			continue
		}
		normalizedKey := normalizeEnvironmentKey(key)
		if index, exists := positions[normalizedKey]; exists {
			merged[index] = item
			continue
		}
		positions[normalizedKey] = len(merged)
		merged = append(merged, item)
	}
	return merged
}

func normalizeEnvironmentKey(key string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(key)
	}
	return key
}
