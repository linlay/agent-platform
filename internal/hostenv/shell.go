package hostenv

import (
	"path/filepath"
	"sort"
	"strings"
)

// BindShellEnvironment reapplies the reviewed command environment after login
// profiles. Only PATH and mounted connector configuration paths are included.
func BindShellEnvironment(shell, command string, env []string, config map[string]string) string {
	keys := []string{"PATH"}
	for key := range config {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	base := strings.TrimSuffix(strings.ToLower(filepath.Base(shell)), ".exe")
	var prefix strings.Builder
	for _, key := range keys {
		value := Value(env, key)
		switch base {
		case "bash", "sh", "zsh", "dash":
			prefix.WriteString("export " + key + "='" + strings.ReplaceAll(value, "'", "'\"'\"'") + "' || exit\n")
		case "powershell", "pwsh":
			prefix.WriteString("$env:" + key + "='" + strings.ReplaceAll(value, "'", "''") + "'; ")
		}
	}
	return prefix.String() + command
}
