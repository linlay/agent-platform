package catalog

import (
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
		"    - datetime\n" +
		"    - ask_user_question\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	for _, tool := range []string{"datetime", "ask_user_question"} {
		if !containsString(def.Tools, tool) {
			t.Fatalf("expected %s in flattened tools list, got %#v", tool, def.Tools)
		}
	}
	if def.MemoryEnabled {
		t.Fatalf("expected memory to stay disabled by default, got %#v", def)
	}
}

func TestParseAgentFileReadsProxyTransport(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: proxy-demo\n" +
		"name: Proxy Demo\n" +
		"mode: PROXY\n" +
		"proxyConfig:\n" +
		"  baseUrl: http://127.0.0.1:3210\n" +
		"  transport: ws\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if def.ProxyConfig == nil || def.ProxyConfig.Transport != "ws" {
		t.Fatalf("expected proxy transport ws, got %#v", def.ProxyConfig)
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
		"    - datetime\n" +
		"  frontends:\n" +
		"    - ask_user_question\n" +
		"  actions:\n" +
		"    - plan_update_task\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	for _, tool := range []string{"_memory_write_", "_memory_read_", "_memory_search_"} {
		if containsString(def.Tools, tool) {
			t.Fatalf("expected default memory tool %s to stay disabled, got %#v", tool, def.Tools)
		}
	}
	for _, tool := range []string{"datetime", "ask_user_question", "plan_update_task"} {
		if containsString(def.Tools, tool) {
			t.Fatalf("expected legacy tool bucket entry %s to stay ignored, got %#v", tool, def.Tools)
		}
	}
	if def.MemoryEnabled {
		t.Fatalf("expected memory to stay disabled by default, got %#v", def)
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
		"    - ask_user_question\n" +
		"  overrides:\n" +
		"    ask_user_question:\n" +
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
	for _, tool := range []string{"ask_user_question"} {
		if !containsString(def.Tools, tool) {
			t.Fatalf("expected %s in flattened tools list, got %#v", tool, def.Tools)
		}
	}
	if def.MemoryEnabled {
		t.Fatalf("expected memory to stay disabled by default, got %#v", def)
	}
	override, ok := def.ToolOverrides["ask_user_question"]
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

func TestParseAgentFileLoadsRuntimeEnv(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: demo\n" +
		"name: Demo\n" +
		"modelConfig:\n" +
		"  modelKey: demo-model\n" +
		"runtimeConfig:\n" +
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
	got, ok := def.Runtime["env"].(map[string]string)
	if !ok {
		t.Fatalf("expected runtime env map[string]string, got %#v", def.Runtime["env"])
	}
	want := map[string]string{
		"HTTP_PROXY": "http://127.0.0.1:7890",
		"EMPTY":      "",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("runtime env = %#v, want %#v", got, want)
	}
}

func TestParseAgentFileFallsBackToLegacySandboxConfig(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: demo\n" +
		"name: Demo\n" +
		"modelConfig:\n" +
		"  modelKey: demo-model\n" +
		"sandboxConfig:\n" +
		"  environmentId: shell\n" +
		"  env:\n" +
		"    HTTP_PROXY: legacy\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if got := def.Runtime["environmentId"]; got != "shell" {
		t.Fatalf("environmentId = %#v, want shell", got)
	}
	got, ok := def.Runtime["env"].(map[string]string)
	if !ok || got["HTTP_PROXY"] != "legacy" {
		t.Fatalf("runtime env = %#v, want legacy HTTP_PROXY", def.Runtime["env"])
	}
}

func TestParseAgentFileRuntimeConfigWinsOverSandboxConfig(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: demo\n" +
		"name: Demo\n" +
		"modelConfig:\n" +
		"  modelKey: demo-model\n" +
		"runtimeConfig:\n" +
		"  environmentId: runtime\n" +
		"  env:\n" +
		"    HTTP_PROXY: runtime\n" +
		"sandboxConfig:\n" +
		"  environmentId: legacy\n" +
		"  env:\n" +
		"    HTTP_PROXY: legacy\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if got := def.Runtime["environmentId"]; got != "runtime" {
		t.Fatalf("environmentId = %#v, want runtime", got)
	}
	got, ok := def.Runtime["env"].(map[string]string)
	if !ok || got["HTTP_PROXY"] != "runtime" {
		t.Fatalf("runtime env = %#v, want runtime HTTP_PROXY", def.Runtime["env"])
	}
}

func TestParseAgentFileSupportsCoderWorkspace(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "project")
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: coder\n" +
		"name: Coder\n" +
		"type: coder\n" +
		"workspaceConfig:\n" +
		"  root: " + filepath.ToSlash(workspace) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if def.Type != AgentTypeCoder {
		t.Fatalf("type = %q, want %q", def.Type, AgentTypeCoder)
	}
	if def.Workspace.Root != filepath.Clean(workspace) {
		t.Fatalf("workspace root = %q, want %q", def.Workspace.Root, filepath.Clean(workspace))
	}
}

func TestParseAgentFileAppliesCoderProfileDefaults(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "project")
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: coder\n" +
		"type: CODER\n" +
		"workspaceConfig:\n" +
		"  root: " + filepath.ToSlash(workspace) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	wantTools := []string{"bash", "file_read", "file_write", "file_edit", "file_grep", "datetime"}
	if !reflect.DeepEqual(def.Tools, wantTools) {
		t.Fatalf("tools = %#v, want %#v", def.Tools, wantTools)
	}
	wantTags := []string{"system", "session", "owner"}
	if !reflect.DeepEqual(def.ContextTags, wantTags) {
		t.Fatalf("context tags = %#v, want %#v", def.ContextTags, wantTags)
	}
	if got := intNode(def.Budget["runTimeoutMs"]); got != 3600000 {
		t.Fatalf("runTimeoutMs = %d, want 3600000", got)
	}
	if got := intNode(mapNode(def.Budget["model"])["maxCalls"]); got != 240 {
		t.Fatalf("model.maxCalls = %d, want 240", got)
	}
	if got := intNode(mapNode(def.Budget["tool"])["maxCalls"]); got != 300 {
		t.Fatalf("tool.maxCalls = %d, want 300", got)
	}
	if def.ReactMaxSteps != 160 {
		t.Fatalf("react max steps = %d, want 160", def.ReactMaxSteps)
	}
	if def.Name != "coder" || def.Role != "coder" || def.Description != "coder" {
		t.Fatalf("identity defaults = name:%q role:%q description:%q, want key fallback", def.Name, def.Role, def.Description)
	}
}

func TestParseAgentFileAllowsCoderProfileOverrides(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "project")
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: coder\n" +
		"type: CODER\n" +
		"toolConfig:\n" +
		"  tools:\n" +
		"    - datetime\n" +
		"contextConfig:\n" +
		"  tags:\n" +
		"    - owner\n" +
		"budget:\n" +
		"  runTimeoutMs: 1234\n" +
		"react:\n" +
		"  maxSteps: 12\n" +
		"workspaceConfig:\n" +
		"  root: " + filepath.ToSlash(workspace) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if !reflect.DeepEqual(def.Tools, []string{"datetime"}) {
		t.Fatalf("tools = %#v, want explicit override", def.Tools)
	}
	if !reflect.DeepEqual(def.ContextTags, []string{"owner"}) {
		t.Fatalf("context tags = %#v, want explicit override", def.ContextTags)
	}
	if got := intNode(def.Budget["runTimeoutMs"]); got != 1234 {
		t.Fatalf("runTimeoutMs = %d, want explicit override", got)
	}
	if def.ReactMaxSteps != 12 {
		t.Fatalf("react max steps = %d, want explicit override", def.ReactMaxSteps)
	}
}

func TestParseAgentFileRejectsCoderWithoutWorkspace(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	if err := os.WriteFile(path, []byte("key: coder\ntype: CODER\n"), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	_, err := parseAgentFile(path)
	if err == nil || !strings.Contains(err.Error(), "workspaceConfig.root is required") {
		t.Fatalf("expected workspace requirement error, got %v", err)
	}
}

func TestParseAgentFileRejectsUnknownType(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	if err := os.WriteFile(path, []byte("key: agent\ntype: REVIEWER\n"), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	_, err := parseAgentFile(path)
	if err == nil || !strings.Contains(err.Error(), "unsupported agent type") {
		t.Fatalf("expected unsupported type error, got %v", err)
	}
}

func TestParseAgentFileRejectsInvalidRuntimeEnv(t *testing.T) {
	tests := []struct {
		name        string
		envValue    any
		errContains string
	}{
		{
			name:        "env must be map",
			envValue:    []any{"HTTP_PROXY"},
			errContains: "runtimeConfig.env must be a map[string]string",
		},
		{
			name: "value must be string",
			envValue: map[string]any{
				"HTTP_PROXY": int64(7890),
			},
			errContains: `runtimeConfig.env["HTTP_PROXY"] must be a string`,
		},
		{
			name: "key must not be empty",
			envValue: map[string]any{
				"": "value",
			},
			errContains: "runtimeConfig.env contains an empty key",
		},
		{
			name: "key must not contain whitespace",
			envValue: map[string]any{
				"BAD KEY": "value",
			},
			errContains: `runtimeConfig.env key "BAD KEY" must not contain whitespace`,
		},
		{
			name: "key must not contain equals",
			envValue: map[string]any{
				"BAD=KEY": "value",
			},
			errContains: `runtimeConfig.env key "BAD=KEY" must not contain '='`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseRuntimeEnv(tt.envValue)
			if err == nil {
				t.Fatal("expected parseRuntimeEnv error")
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

func TestParseAgentFileKeepsBaseMemoryToolsDisabledByDefault(t *testing.T) {
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
		if containsString(def.Tools, tool) {
			t.Fatalf("expected %s to stay disabled by default, got %#v", tool, def.Tools)
		}
	}
	if def.MemoryEnabled {
		t.Fatalf("expected memory disabled by default, got %#v", def)
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

func TestParseAgentFileParsesMemoryRuntimeConfig(t *testing.T) {
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
		"  embedding:\n" +
		"    providerKey: openai\n" +
		"    model: text-embedding-3-small\n" +
		"    dimension: 1536\n" +
		"    timeoutMs: 15000\n" +
		"  autoRemember:\n" +
		"    enabled: true\n" +
		"    modelKey: minimax-m2_7-anthropic\n" +
		"    timeoutMs: 60000\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if !def.MemoryConfig.Enabled || !def.MemoryEnabled {
		t.Fatalf("expected memory enabled, got %#v", def.MemoryConfig)
	}
	if def.MemoryConfig.Embedding.ProviderKey != "openai" ||
		def.MemoryConfig.Embedding.Model != "text-embedding-3-small" ||
		def.MemoryConfig.Embedding.Dimension != 1536 ||
		def.MemoryConfig.Embedding.TimeoutMs != 15000 {
		t.Fatalf("unexpected embedding config: %#v", def.MemoryConfig.Embedding)
	}
	if !def.MemoryConfig.AutoRemember.Enabled ||
		def.MemoryConfig.AutoRemember.ModelKey != "minimax-m2_7-anthropic" ||
		def.MemoryConfig.AutoRemember.TimeoutMs != 60000 {
		t.Fatalf("unexpected auto remember config: %#v", def.MemoryConfig.AutoRemember)
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

func TestParseAgentFileWithPromptsLoadsLegacySoulSections(t *testing.T) {
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

	def, err := parseAgentFileWithPrompts(path, root)
	if err != nil {
		t.Fatalf("parse agent file with prompts: %v", err)
	}
	if !strings.Contains(def.SoulPrompt, "Legacy mission") {
		t.Fatalf("expected soul prompt to load, got %q", def.SoulPrompt)
	}
	if !strings.Contains(def.SoulPrompt, "# Identity") || !strings.Contains(def.SoulPrompt, "## Mission") {
		t.Fatalf("expected legacy headings to remain in soul prompt, got %q", def.SoulPrompt)
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
