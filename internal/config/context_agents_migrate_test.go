package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIgnoredAgentContextMigrationOptionalAndIdempotent(t *testing.T) {
	root := t.TempDir()
	agents := filepath.Join(root, "agents")
	if err := os.MkdirAll(filepath.Join(agents, "demo"), 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(agents, "demo", "agent.yml")
	before := "# retain header\nkey: demo\nmodelConfig: {modelKey: test}\ncontextConfig:\n  tags:\n    - system\n    - agents\n    - owner\n  agents:\n    invalid: type\nruntimeConfig:\n  workspaceRoot: /example\n"
	if err := os.WriteFile(path, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunConfigMigration([]string{"--config-dir", root, "--agents-dir", agents}, &out); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != before || !strings.Contains(out.String(), path) {
		t.Fatalf("preview mutated source or missed file: %s", out.String())
	}
	if err := RunConfigMigration([]string{"--config-dir", root, "--agents-dir", agents, "--apply"}, &out); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if strings.Contains(string(data), "agents:") || strings.Contains(string(data), "- agents") || !strings.Contains(string(data), "/example") || !strings.Contains(string(data), "# retain header") {
		t.Fatalf("cleanup=%s", data)
	}
	tree, err := LoadYAMLTreeBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	ctx := tree.(map[string]any)["contextConfig"].(map[string]any)
	tags := ctx["tags"].([]any)
	if len(tags) != 2 || tags[0] != "system" || tags[1] != "owner" {
		t.Fatalf("tags=%#v", tags)
	}
	if err := RunConfigMigration([]string{"--config-dir", root, "--agents-dir", agents, "--apply"}, &out); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(path)
	if !bytes.Equal(data, again) {
		t.Fatal("cleanup is not idempotent")
	}
}
