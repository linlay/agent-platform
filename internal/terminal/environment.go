package terminal

import (
	"os"
	"runtime"
	"strings"

	"agent-platform/internal/agentconfig"
	"agent-platform/internal/builtins"
	"agent-platform/internal/shellenv"
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

func processEnvironment(req startPTYRequest) []string {
	env := req.Env
	if !req.Managed {
		env = shellenv.StripLocator(mergeEnvironment(os.Environ(), req.Env))
	}
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(key, agentconfig.EnvChatDir) && !strings.EqualFold(key, agentconfig.EnvAccessToken) {
			filtered = append(filtered, entry)
		}
	}
	return builtins.EnsureBinInEnv(filtered)
}
