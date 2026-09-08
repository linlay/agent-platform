package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ResolveStateDir gives standalone connector management the same environment
// contract as Platform startup, without loading unrelated runtime settings.
func ResolveStateDir(configRoot, runtimeRoot string) (string, error) {
	return resolveStatePath(configRoot, pathEnv("AP_RUNTIME_STATE_DIR", filepath.Join(runtimeRoot, ".state")))
}

func resolveStatePath(configRoot, value string) (string, error) {
	value, err := expandPathHome(value, "AP_RUNTIME_STATE_DIR")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(resolveConfigRoot(configRoot), value)
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve AP_RUNTIME_STATE_DIR: %w", err)
	}
	return filepath.Clean(abs), nil
}

// EffectiveStateDir is the Platform-wide persistent operational state root.
// Modules own their namespace beneath it; business data keeps its own roots.
func (p PathsConfig) EffectiveStateDir() string {
	if value := strings.TrimSpace(p.StateDir); value != "" {
		return value
	}
	for _, candidate := range []string{p.AgentsDir, p.RegistriesDir, p.ConnectorsCenterDir} {
		if value := strings.TrimSpace(candidate); value != "" {
			return filepath.Join(filepath.Dir(value), ".state")
		}
	}
	return ""
}
