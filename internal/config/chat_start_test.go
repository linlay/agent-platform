package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestChatStartPermissionOptIn(t *testing.T) {
	for _, tc := range []struct {
		yaml         string
		enabled, bad bool
	}{
		{"", false, false},
		{"runQuery: {}", false, false},
		{"runQuery:\n  allowAccessLevelOverride: false", false, false},
		{"runQuery:\n  allowAccessLevelOverride: true", true, false},
		{"runQuery: true", false, true},
		{"runQuery:\n  allowAccessLevelOverride: 'enabled'", false, true},
		{"runQuery:\n  typo: true", false, true},
	} {
		t.Run(tc.yaml, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tools.yml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0600); err != nil {
				t.Fatal(err)
			}
			cfg := Config{RunQuery: RunQueryConfig{AllowAccessLevelOverride: true}}
			err := cfg.applyToolsFile(path, false)
			if (err != nil) != tc.bad || (!tc.bad && cfg.RunQuery.AllowAccessLevelOverride != tc.enabled) {
				t.Fatalf("config=%#v err=%v", cfg.RunQuery, err)
			}
		})
	}
}
