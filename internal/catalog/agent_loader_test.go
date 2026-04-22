package catalog

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseAgentFileSupportsFlattenedToolConfig(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: demo\n" +
		"name: Demo\n" +
		"mode: REACT\n" +
		"modelConfig:\n" +
		"  modelKey: demo-model\n" +
		"toolConfig:\n" +
		"  tools:\n" +
		"    - _datetime_\n" +
		"    - _ask_user_question_\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	for _, tool := range []string{"_datetime_", "_ask_user_question_", "_memory_write_", "_memory_read_", "_memory_search_"} {
		if !containsString(def.Tools, tool) {
			t.Fatalf("expected %s in flattened tools list, got %#v", tool, def.Tools)
		}
	}
}

func TestParseAgentFileIgnoresLegacyToolConfigBuckets(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: demo\n" +
		"name: Demo\n" +
		"mode: REACT\n" +
		"modelConfig:\n" +
		"  modelKey: demo-model\n" +
		"toolConfig:\n" +
		"  backends:\n" +
		"    - _datetime_\n" +
		"  frontends:\n" +
		"    - _ask_user_question_\n" +
		"  actions:\n" +
		"    - _plan_update_task_\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	for _, tool := range []string{"_memory_write_", "_memory_read_", "_memory_search_"} {
		if !containsString(def.Tools, tool) {
			t.Fatalf("expected default memory tool %s, got %#v", tool, def.Tools)
		}
	}
	for _, tool := range []string{"_datetime_", "_ask_user_question_", "_plan_update_task_"} {
		if containsString(def.Tools, tool) {
			t.Fatalf("expected legacy tool bucket entry %s to stay ignored, got %#v", tool, def.Tools)
		}
	}
}

func TestParseAgentFileLoadsToolOverridesFromToolConfig(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: demo\n" +
		"name: Demo\n" +
		"mode: REACT\n" +
		"modelConfig:\n" +
		"  modelKey: demo-model\n" +
		"toolConfig:\n" +
		"  tools:\n" +
		"    - _ask_user_question_\n" +
		"  overrides:\n" +
		"    _ask_user_question_:\n" +
		"      label: Ask\n" +
		"      description: Ask the user a question\n" +
		"      viewportType: builtin\n" +
		"      viewportKey: question_dialog\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	for _, tool := range []string{"_ask_user_question_", "_memory_write_", "_memory_read_", "_memory_search_"} {
		if !containsString(def.Tools, tool) {
			t.Fatalf("expected %s in flattened tools list, got %#v", tool, def.Tools)
		}
	}
	override, ok := def.ToolOverrides["_ask_user_question_"]
	if !ok {
		t.Fatalf("expected tool override to load, got %#v", def.ToolOverrides)
	}
	if override.Label != "Ask" || override.Description != "Ask the user a question" {
		t.Fatalf("expected tool override fields to load, got %#v", override)
	}
	if override.Meta["viewportType"] != "builtin" || override.Meta["viewportKey"] != "question_dialog" {
		t.Fatalf("expected viewport metadata in tool override meta, got %#v", override.Meta)
	}
}

func TestParseAgentFileLoadsSandboxEnv(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: demo\n" +
		"name: Demo\n" +
		"modelConfig:\n" +
		"  modelKey: demo-model\n" +
		"sandboxConfig:\n" +
		"  env:\n" +
		"    HTTP_PROXY: http://127.0.0.1:7890\n" +
		"    EMPTY: \"\"\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	got, ok := def.Sandbox["env"].(map[string]string)
	if !ok {
		t.Fatalf("expected sandbox env map[string]string, got %#v", def.Sandbox["env"])
	}
	want := map[string]string{
		"HTTP_PROXY": "http://127.0.0.1:7890",
		"EMPTY":      "",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sandbox env = %#v, want %#v", got, want)
	}
}

func TestParseAgentFileRejectsInvalidSandboxEnv(t *testing.T) {
	tests := []struct {
		name        string
		envValue    any
		errContains string
	}{
		{
			name:        "env must be map",
			envValue:    []any{"HTTP_PROXY"},
			errContains: "sandboxConfig.env must be a map[string]string",
		},
		{
			name: "value must be string",
			envValue: map[string]any{
				"HTTP_PROXY": int64(7890),
			},
			errContains: `sandboxConfig.env["HTTP_PROXY"] must be a string`,
		},
		{
			name: "key must not be empty",
			envValue: map[string]any{
				"": "value",
			},
			errContains: "sandboxConfig.env contains an empty key",
		},
		{
			name: "key must not contain whitespace",
			envValue: map[string]any{
				"BAD KEY": "value",
			},
			errContains: `sandboxConfig.env key "BAD KEY" must not contain whitespace`,
		},
		{
			name: "key must not contain equals",
			envValue: map[string]any{
				"BAD=KEY": "value",
			},
			errContains: `sandboxConfig.env key "BAD=KEY" must not contain '='`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseSandboxEnv(tt.envValue)
			if err == nil {
				t.Fatal("expected parseSandboxEnv error")
			}
			if !strings.Contains(err.Error(), tt.errContains) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.errContains)
			}
		})
	}
}

func TestParseAgentFileInjectsMemoryManagementToolsOnlyWhenEnabled(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: demo\n" +
		"name: Demo\n" +
		"mode: REACT\n" +
		"modelConfig:\n" +
		"  modelKey: demo-model\n" +
		"memoryConfig:\n" +
		"  enabled: true\n" +
		"  managementTools: true\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	want := []string{
		"_memory_write_",
		"_memory_read_",
		"_memory_search_",
		"_memory_update_",
		"_memory_forget_",
		"_memory_timeline_",
		"_memory_promote_",
		"_memory_consolidate_",
	}
	for _, tool := range want {
		if !containsString(def.Tools, tool) {
			t.Fatalf("expected %s in tools, got %#v", tool, def.Tools)
		}
	}
}

func TestParseAgentFileInjectsBaseMemoryToolsByDefault(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: demo\n" +
		"name: Demo\n" +
		"mode: REACT\n" +
		"modelConfig:\n" +
		"  modelKey: demo-model\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	for _, tool := range []string{"_memory_write_", "_memory_read_", "_memory_search_"} {
		if !containsString(def.Tools, tool) {
			t.Fatalf("expected %s in tools by default, got %#v", tool, def.Tools)
		}
	}
}

func TestParseAgentFileKeepsMemoryManagementToolsOptIn(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: demo\n" +
		"name: Demo\n" +
		"mode: REACT\n" +
		"modelConfig:\n" +
		"  modelKey: demo-model\n" +
		"memoryConfig:\n" +
		"  enabled: true\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	for _, tool := range []string{"_memory_update_", "_memory_forget_", "_memory_timeline_", "_memory_promote_"} {
		if containsString(def.Tools, tool) {
			t.Fatalf("expected %s to stay opt-in, got %#v", tool, def.Tools)
		}
	}
}

func TestParseAgentFileAllowsOptingOutOfBaseMemoryTools(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: demo\n" +
		"name: Demo\n" +
		"mode: REACT\n" +
		"modelConfig:\n" +
		"  modelKey: demo-model\n" +
		"memoryConfig:\n" +
		"  enabled: false\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	for _, tool := range []string{"_memory_write_", "_memory_read_", "_memory_search_"} {
		if containsString(def.Tools, tool) {
			t.Fatalf("expected %s to stay disabled, got %#v", tool, def.Tools)
		}
	}
}

func TestParseAgentFileWithPromptsWarnsOnLegacySoulSections(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	soulPath := filepath.Join(root, "SOUL.md")
	content := "" +
		"key: demo\n" +
		"name: Demo\n" +
		"role: Demo role\n" +
		"description: Demo description\n" +
		"mode: REACT\n" +
		"modelConfig:\n" +
		"  modelKey: demo-model\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}
	if err := os.WriteFile(soulPath, []byte("# Identity\n\n- key: demo\n\n## Mission\n\nLegacy mission"), 0o644); err != nil {
		t.Fatalf("write soul file: %v", err)
	}

	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(previous)

	def, err := parseAgentFileWithPrompts(path, root)
	if err != nil {
		t.Fatalf("parse agent file with prompts: %v", err)
	}
	if !strings.Contains(def.SoulPrompt, "Legacy mission") {
		t.Fatalf("expected soul prompt to load, got %q", def.SoulPrompt)
	}
	logOutput := buf.String()
	if !strings.Contains(logOutput, "legacy SOUL.md headings") {
		t.Fatalf("expected legacy SOUL warning, got %q", logOutput)
	}
	if !strings.Contains(logOutput, "# Identity, ## Mission") {
		t.Fatalf("expected warning to mention legacy headings, got %q", logOutput)
	}
}

func TestParseAgentFileWithPromptsLoadsWithoutSoulFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: demo\n" +
		"name: Demo\n" +
		"role: Demo role\n" +
		"description: Demo description\n" +
		"mode: REACT\n" +
		"modelConfig:\n" +
		"  modelKey: demo-model\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFileWithPrompts(path, root)
	if err != nil {
		t.Fatalf("parse agent file with prompts: %v", err)
	}
	if def.SoulPrompt != "" {
		t.Fatalf("expected empty soul prompt when SOUL.md is missing, got %q", def.SoulPrompt)
	}
	if def.Key != "demo" || def.Name != "Demo" || def.Role != "Demo role" || def.Description != "Demo description" {
		t.Fatalf("expected identity fields from agent.yml, got %#v", def)
	}
}
