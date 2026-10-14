package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRejectEditingPromptAndVariable(t *testing.T) {
	for _, body := range []string{
		"kbase:\n  editing-prompt: old\n",
		"kbase:\n  workspace-prompt: '{{editing_mode}}'\n",
		"coder:\n  system-prompt: '{{ editing_mode }}'\n",
		"shared:\n  planning-mode:\n    planning-prompt: '{{editing_mode}}'\n",
	} {
		cfg := Config{KBasePrompts: KBasePromptsConfig{WorkspacePrompt: "current"}}
		err := cfg.applyAgentPromptFile(configFixture(t, "agent-prompt.yml", body))
		if err == nil || !(strings.Contains(err.Error(), "editing-prompt") || strings.Contains(err.Error(), "editing_mode")) {
			t.Fatalf("retired prompt configuration accepted: %v", err)
		}
		if cfg.KBasePrompts.WorkspacePrompt != "current" {
			t.Fatal("rejected configuration modified the active prompt")
		}
	}
}

func TestMigrationRetiresKBaseEditingPromptWithoutChangingModels(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "configs")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	prompt := "kbase:\n  system-prompt: |\n    Keep my system instructions\n  workspace-prompt: |\n    Keep workspace instructions\n  editing-prompt: |\n    Legacy instructions\n"
	settings := "general:\n  default-agent:\n    modelKey: user-model\n    reasoningEffort: HIGH\n"
	for name, text := range map[string]string{"agent-prompt.yml": prompt, "agent-settings.yml": settings} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := RunConfigMigration([]string{"--config-dir", root}, &out); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "agent-prompt.yml"))
	if string(before) != prompt {
		t.Fatal("preview modified prompts")
	}
	if err := RunConfigMigration([]string{"--config-dir", root, "--apply"}, &out); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "agent-prompt.yml"))
	if strings.Contains(string(after), "editing-prompt") || !strings.Contains(string(after), "Keep my system instructions") || !strings.Contains(string(after), "Keep workspace instructions") {
		t.Fatal("migration changed active prompts")
	}
	model, _ := os.ReadFile(filepath.Join(dir, "agent-settings.yml"))
	if string(model) != settings {
		t.Fatal("migration changed model or reasoning settings")
	}
	backups, _ := filepath.Glob(filepath.Join(root, "config-backups", "*", "*"))
	retained := false
	for _, path := range backups {
		data, _ := os.ReadFile(path)
		if bytes.Equal(data, []byte(prompt)) {
			retained = true
		}
	}
	if !retained {
		t.Fatal("original prompt missing from migration backup")
	}
	var c Config
	if err := c.applyStructuredConfig(root, false); err != nil {
		t.Fatal(err)
	}
}
