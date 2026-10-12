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

// EffectiveStateDir is the credential and identity root: connector credentials
// and login state, the identity ticket, and managed configuration that carries
// model keys. Run state, receipts and progress records do not belong here.
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

// EffectiveRUPreviewsDir holds the records of document preview copies kept on
// the preview service. It persists across restarts so those copies can be
// reclaimed, and is protected because each record carries a share link.
func (p PathsConfig) EffectiveRUPreviewsDir() string {
	if value := strings.TrimSpace(p.RUPreviewsDir); value != "" {
		return value
	}
	if state := p.EffectiveStateDir(); state != "" {
		return filepath.Join(filepath.Dir(state), "ru-previews")
	}
	return ""
}
