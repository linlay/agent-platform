package config

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func configFixture(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestMergedAgentSettingsAndPresets(t *testing.T) {
	path := configFixture(t, "agent-settings.yml", `preset-tools:
  - datetime
  - wait
preset-connectors:
  - builtin.web-control
coder:
  preset-tools:
    - wait
    - file_read
  preset-connectors:
    - builtin.web-control
    - builtin.httpx
  default-agent:
    modelKey: code-model
    reasoningEffort: high
  workspace-agents:
    file: RULES.md
kbase:
  default-agent:
    modelKey: answer-model
    reasoningEffort: medium
acp-bridges:
  local:
    base-url: http://127.0.0.1:17071
`)
	var cfg Config
	if err := cfg.applyAgentSettingsFile(path); err != nil {
		t.Fatal(err)
	}
	if got := cfg.PresetsForMode("CODER"); !reflect.DeepEqual(got.Tools, []string{"datetime", "wait", "file_read"}) || !reflect.DeepEqual(got.Connectors, []string{"builtin.web-control", "builtin.httpx"}) {
		t.Fatal(got)
	}
	if cfg.GeneralSettings.WorkspaceAgents.Enabled || cfg.GeneralSettings.WorkspaceAgents.File != "" {
		t.Fatal("absent file must not enable rules")
	}
	if !cfg.CoderSettings.WorkspaceAgents.Enabled || cfg.CoderSettings.WorkspaceAgents.File != "RULES.md" || cfg.KBase.DefaultAgent.ModelKey != "answer-model" || cfg.ACP.SourcePath != path {
		t.Fatal(cfg)
	}
	if err := cfg.applyToolsFile(configFixture(t, "tools.yml", "bash: {}\n"), false); err != nil {
		t.Fatal(err)
	}
	if len(cfg.PresetsForMode("coder").Tools) != 3 {
		t.Fatal("tools reset presets")
	}
}
func TestMergedConfigRejectsInvalidAndRetiredFields(t *testing.T) {
	for _, body := range []string{
		"coder:\n  workspace-agents:\n    enabled: true\n    file: AGENTS.md\n",
		"general:\n  workspace-agents: {}\n",
		"kbase:\n  embedding:\n    modelKey: old\n",
		"kbase:\n  index: {}\n",
		"preset-tools: null\n", "coder: []\n", "coder: {}\ncoder: {}\n",
	} {
		t.Run(body, func(t *testing.T) {
			var c Config
			if err := c.applyAgentSettingsFile(configFixture(t, "settings.yml", body)); err == nil {
				t.Fatal("accepted invalid config")
			}
		})
	}
	for _, body := range []string{"preset-tools: []\n", "preset-connectors: []\n", "vision-recognize:\n  profiles:\n    arbitrary-name:\n      typo: x\n"} {
		var c Config
		if err := c.applyToolsFile(configFixture(t, "tools.yml", body), false); err == nil {
			t.Fatal(body)
		}
	}
}
func TestMergedPromptsAndKBX(t *testing.T) {
	var c Config
	body := "shared:\n  skill:\n    instructions-prompt: |\n      exact {{placeholder}}\n      second line\ncoder:\n  planning-prompt: plan\nkbase:\n  system-prompt: answer\n"
	if err := c.applyAgentPromptFile(configFixture(t, "agent-prompt.yml", body)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.Prompts.Skill.InstructionsPrompt, "exact {{placeholder}}\nsecond line") || c.CoderPrompts.PlanningPrompt != "plan" || c.KBasePrompts.SystemPrompt != "answer" {
		t.Fatal(c)
	}
	if err := c.applyRuntimeFile(configFixture(t, "runtime.yml", "kbx:\n  embedding:\n    model-key: embed\n    prompt: qwen3\n")); err != nil {
		t.Fatal(err)
	}
	if c.KBX.Embedding.ModelKey != "embed" || c.KBX.Embedding.Prompt != "qwen3" {
		t.Fatal(c.KBX)
	}
}
func TestMigrationPreservesRawValuesAndRejectsConflicts(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "configs")
	os.Mkdir(dir, 0700)
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("coder-settings.yml", "default-agent:\n  modelKey: code\nworkspace-agents:\n  enabled: true\n  file: AGENTS.md\nacp-bridges:\n  local:\n    base-url: http://127.0.0.1:17071\n    auth-token: ${PRIVATE_TOKEN:}\n    desktop-plugin-id: plugin\n")
	write("coder-prompts.yml", "system-prompt: |\n  Hello {{name}}\n  # literal markdown\n  second line\n")
	write("tools.yml", "preset-tools:\n  - wait\nbash:\n  max-command-chars: 9000\n")
	write("ai-tools.yml", "vision-recognize:\n  enabled: false\n")
	write("kbase-settings.yml", "default-agent:\n  modelKey: answer\nembedding:\n  modelKey: embed\n  prompt: qwen3\nindex:\n  fts:\n    base-tokenizer: icu\n")
	var out bytes.Buffer
	if err := RunConfigMigration([]string{"--config-dir", root}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "agent-settings.yml")); !os.IsNotExist(err) {
		t.Fatal("preview wrote files")
	}
	if strings.Contains(out.String(), "PRIVATE_TOKEN") {
		t.Fatal("preview leaked values")
	}
	if err := RunConfigMigration([]string{"--config-dir", root, "--apply"}, &out); err != nil {
		t.Fatal(err)
	}
	var c Config
	if err := c.applyStructuredConfig(root, false); err != nil {
		t.Fatal(err)
	}
	if c.CoderSettings.DefaultAgent.ModelKey != "code" || c.KBX.Embedding.ModelKey != "embed" || len(c.PresetTools) != 1 {
		t.Fatal("migration changed values")
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "agent-settings.yml"))
	if !bytes.Contains(raw, []byte("${PRIVATE_TOKEN:}")) {
		t.Fatal("expanded secret expression")
	}
	raw, _ = os.ReadFile(filepath.Join(dir, "agent-prompt.yml"))
	if !bytes.Contains(raw, []byte("# literal markdown")) {
		t.Fatal("lost prompt text")
	}
	write("coder-settings.yml", "default-agent:\n  modelKey: conflict\n")
	if err := RunConfigMigration([]string{"--config-dir", root, "--apply"}, &out); err == nil {
		t.Fatal("accepted conflicting mode")
	}
	if err := c.applyStructuredConfig(root, false); err != nil {
		t.Fatalf("legacy file must not block loading: %v", err)
	}
}

func TestMigrationAgentModelConflictAndBackup(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "configs")
	agents := filepath.Join(root, "agents")
	for _, p := range []string{dir, filepath.Join(agents, "docs")} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	old := []byte("default-agent:\n  modelKey: answer\nembedding:\n  modelKey: embed\n")
	if err := os.WriteFile(filepath.Join(dir, "kbase-settings.yml"), old, 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(agents, "docs", "agent.yml")
	agent := "key: docs\nmode: KBASE\nkbaseConfig:\n  embedding:\n    modelKey: other\n  include:\n    - '**/*.md'\n"
	if err := os.WriteFile(path, []byte(agent), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunConfigMigration([]string{"--config-dir", root, "--agents-dir", agents, "--apply"}, &out); err == nil {
		t.Fatal("different Agent model migrated silently")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "kbase-settings.yml"))
	if !bytes.Equal(after, old) {
		t.Fatal("failed migration changed source")
	}
	agent = strings.Replace(agent, "modelKey: other", "modelKey: embed", 1)
	if err := os.WriteFile(path, []byte(agent), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RunConfigMigration([]string{"--config-dir", root, "--agents-dir", agents, "--apply"}, &out); err != nil {
		t.Fatal(err)
	}
	after, _ = os.ReadFile(path)
	if bytes.Contains(after, []byte("embedding:")) || !bytes.Contains(after, []byte("**/*.md")) {
		t.Fatal("Agent mutation lost filters or retained model")
	}
	backups, _ := filepath.Glob(filepath.Join(root, "config-backups", "*", "*kbase-settings.yml"))
	if len(backups) != 1 {
		t.Fatal(backups)
	}
	saved, _ := os.ReadFile(backups[0])
	if !bytes.Equal(saved, old) {
		t.Fatal("backup changed bytes")
	}
	before, _ := os.ReadFile(filepath.Join(dir, "agent-settings.yml"))
	if err := RunConfigMigration([]string{"--config-dir", root, "--agents-dir", agents, "--apply"}, &out); err != nil {
		t.Fatal(err)
	}
	after, _ = os.ReadFile(filepath.Join(dir, "agent-settings.yml"))
	if !bytes.Equal(before, after) {
		t.Fatal("second migration changed config")
	}
}

func TestMigrationCanFinishAgentsAfterConfigWasMigrated(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "configs")
	agents := filepath.Join(root, "agents")
	os.MkdirAll(dir, 0700)
	os.MkdirAll(filepath.Join(agents, "docs"), 0700)
	os.WriteFile(filepath.Join(dir, "runtime.yml"), []byte("kbx:\n  embedding:\n    model-key: embed\n"), 0600)
	path := filepath.Join(agents, "docs", "agent.yml")
	os.WriteFile(path, []byte("key: docs\nkbaseConfig:\n  embedding:\n    modelKey: embed\n"), 0600)
	var out bytes.Buffer
	if err := RunConfigMigration([]string{"--config-dir", root, "--agents-dir", agents, "--apply"}, &out); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if bytes.Contains(b, []byte("embedding")) {
		t.Fatal("retired Agent embedding remained")
	}
}

func TestMigrationModelIdentityPreservesUnresolvedExpressions(t *testing.T) {
	a := migrationModelIdentity(YAMLSourceValue{Head: "${EMBED_A:}"})
	b := migrationModelIdentity(YAMLSourceValue{Head: "${EMBED_B:}"})
	if a == "" || a == b {
		t.Fatal("distinct unresolved model declarations collapsed")
	}
}

func TestStructuredConfigIgnoresRetiredFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "configs")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range retiredAgentFiles {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("invalid: [\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "agent-settings.yml"), []byte("general:\n  default-agent:\n    modelKey: current-model\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := cfg.applyStructuredConfig(root, false); err != nil {
		t.Fatal(err)
	}
	if cfg.GeneralSettings.DefaultAgent.ModelKey != "current-model" {
		t.Fatal("new settings not loaded")
	}
}
