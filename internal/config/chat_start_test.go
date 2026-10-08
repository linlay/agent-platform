package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChatStartOverrideSwitchIsRetired(t *testing.T) {
	for _, tc := range []struct {
		yaml string
		bad  bool
	}{
		{"", false},
		{"bash: {}", false},
		{"runQuery: {}", true},
		{"runQuery:\n  allowAccessLevelOverride: false", true},
		{"runQuery:\n  allowAccessLevelOverride: true", true},
	} {
		t.Run(tc.yaml, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tools.yml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0600); err != nil {
				t.Fatal(err)
			}
			cfg := Config{}
			err := cfg.applyToolsFile(path, false)
			if (err != nil) != tc.bad {
				t.Fatalf("err=%v", err)
			}
			if tc.bad && (!strings.Contains(err.Error(), "runQuery was removed") || !strings.Contains(err.Error(), "config-migrate")) {
				t.Fatalf("missing migration hint: %v", err)
			}
		})
	}
}

func TestConfigMigrationRemovesChatStartOverrideSwitch(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "configs"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "configs", "tools.yml")
	if err := os.WriteFile(path, []byte("runQuery:\n  allowAccessLevelOverride: true\nbash:\n  default-timeout-ms: 1000\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunConfigMigration([]string{"--config-dir", dir, "--apply"}, &out); err != nil {
		t.Fatalf("migrate: %v\n%s", err, out.String())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "runQuery") || !strings.Contains(string(data), "default-timeout-ms") {
		t.Fatalf("tools.yml=%s", data)
	}
	if !strings.Contains(out.String(), "runQuery.allowAccessLevelOverride") {
		t.Fatalf("missing note: %s", out.String())
	}
	if err := (&Config{}).applyToolsFile(path, false); err != nil {
		t.Fatalf("migrated file rejected: %v", err)
	}
}
