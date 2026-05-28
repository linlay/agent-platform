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

func TestParseAgentFileDefaultsProxyTransportToWebSocket(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: proxy-demo\n" +
		"name: Proxy Demo\n" +
		"mode: PROXY\n" +
		"proxyConfig:\n" +
		"  baseUrl: http://127.0.0.1:3210\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if def.ProxyConfig == nil || def.ProxyConfig.Transport != "ws" {
		t.Fatalf("expected default proxy transport ws, got %#v", def.ProxyConfig)
	}
}

func TestParseAgentFileKeepsExplicitProxySSETransport(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: proxy-demo\n" +
		"name: Proxy Demo\n" +
		"mode: PROXY\n" +
		"proxyConfig:\n" +
		"  baseUrl: http://127.0.0.1:3210\n" +
		"  transport: sse\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if def.ProxyConfig == nil || def.ProxyConfig.Transport != "sse" {
		t.Fatalf("expected explicit proxy transport sse, got %#v", def.ProxyConfig)
	}
}

func TestParseAgentFileDefaultsModeVisibilityAndKanban(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: demo\n" +
		"name: Demo\n" +
		"modelConfig:\n" +
		"  modelKey: demo-model\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if def.Mode != "REACT" {
		t.Fatalf("mode = %q, want REACT", def.Mode)
	}
	if !reflect.DeepEqual(def.VisibilityScopes, []string{"nav"}) {
		t.Fatalf("visibility scopes = %#v", def.VisibilityScopes)
	}
	if def.KanbanConcurrency != 1 {
		t.Fatalf("kanban concurrency = %d, want 1", def.KanbanConcurrency)
	}
}

func TestParseAgentFileDefaultsEmptyOrInvalidVisibilityToNav(t *testing.T) {
	for _, tc := range []struct {
		name       string
		visibility string
	}{
		{name: "empty scopes", visibility: "visibility:\n  scopes: []\n"},
		{name: "invalid scopes", visibility: "visibility:\n  scopes:\n    - nope\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "agent.yml")
			content := "" +
				"key: demo\n" +
				"name: Demo\n" +
				"modelConfig:\n" +
				"  modelKey: demo-model\n" +
				tc.visibility
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatalf("write agent file: %v", err)
			}

			def, err := parseAgentFile(path)
			if err != nil {
				t.Fatalf("parse agent file: %v", err)
			}
			if !reflect.DeepEqual(def.VisibilityScopes, []string{"nav"}) {
				t.Fatalf("visibility scopes = %#v", def.VisibilityScopes)
			}
		})
	}
}

func TestParseAgentFileAcceptsPlanExecuteModeAliases(t *testing.T) {
	for _, mode := range []string{"PLAN-EXECUTE", "PLAN_EXECUTE"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "agent.yml")
			content := "" +
				"key: demo\n" +
				"name: Demo\n" +
				"mode: " + mode + "\n" +
				"modelConfig:\n" +
				"  modelKey: demo-model\n"
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatalf("write agent file: %v", err)
			}

			def, err := parseAgentFile(path)
			if err != nil {
				t.Fatalf("parse agent file: %v", err)
			}
			if def.Mode != "PLAN_EXECUTE" {
				t.Fatalf("mode = %q, want PLAN_EXECUTE", def.Mode)
			}
		})
	}
}

func TestParseAgentFileReadsVisibilityAndKanban(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: demo\n" +
		"name: Demo\n" +
		"mode: coder\n" +
		"modelConfig:\n" +
		"  modelKey: demo-model\n" +
		"runtimeConfig:\n" +
		"  environmentId: shell\n" +
		"visibility:\n" +
		"  scopes:\n" +
		"    - internal\n" +
		"    - invoke\n" +
		"    - bad-scope\n" +
		"    - invoke\n" +
		"kanban:\n" +
		"  concurrency: 3\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if def.Mode != AgentModeCoder {
		t.Fatalf("mode = %q, want %q", def.Mode, AgentModeCoder)
	}
	if !reflect.DeepEqual(def.VisibilityScopes, []string{"internal", "invoke"}) {
		t.Fatalf("visibility scopes = %#v", def.VisibilityScopes)
	}
	if def.KanbanConcurrency != 3 {
		t.Fatalf("kanban concurrency = %d, want 3", def.KanbanConcurrency)
	}
}

func TestParseAgentFileRejectsInvalidKanbanConcurrency(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: demo\n" +
		"name: Demo\n" +
		"kanban:\n" +
		"  concurrency: 0\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	_, err := parseAgentFile(path)
	if err == nil || !strings.Contains(err.Error(), "kanban.concurrency") {
		t.Fatalf("expected kanban concurrency error, got %v", err)
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
	for _, tool := range []string{"memory_write", "memory_read", "memory_search"} {
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

func TestParseAgentFileUsesRuntimeConfig(t *testing.T) {
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
		"    HTTP_PROXY: runtime\n"
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
		"mode: coder\n" +
		"runtimeConfig:\n" +
		"  workspaceRoot: " + filepath.ToSlash(workspace) + "\n" +
		"projectConfig:\n" +
		"  promptFiles:\n" +
		"    - source: workspace\n" +
		"      path: AGENTS.md\n" +
		"    - source: agent\n" +
		"      path: AGENTS.md\n" +
		"  git:\n" +
		"    expectedBranch: main\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if def.Mode != AgentModeCoder {
		t.Fatalf("mode = %q, want %q", def.Mode, AgentModeCoder)
	}
	if def.Workspace.Root != filepath.Clean(workspace) {
		t.Fatalf("workspace root = %q, want %q", def.Workspace.Root, filepath.Clean(workspace))
	}
	wantPromptFiles := []AgentProjectPromptFile{
		{Source: "workspace", Path: "AGENTS.md"},
		{Source: "agent", Path: "AGENTS.md"},
	}
	if !reflect.DeepEqual(def.Project.PromptFiles, wantPromptFiles) {
		t.Fatalf("project prompt files = %#v", def.Project.PromptFiles)
	}
	if def.Project.Git.ExpectedBranch != "main" {
		t.Fatalf("expected branch = %q, want main", def.Project.Git.ExpectedBranch)
	}
}

func TestParseAgentFileSupportsACPCoderBackend(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "project")
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: coder\n" +
		"mode: CODER\n" +
		"runtimeConfig:\n" +
		"  coderBackend: acp\n" +
		"  acpProxyId: codex\n" +
		"  workspaceRoot: " + filepath.ToSlash(workspace) + "\n" +
		"projectConfig:\n" +
		"  git:\n" +
		"    expectedBranch: main\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if def.Mode != AgentModeCoder || def.CoderBackend != AgentCoderBackendACP {
		t.Fatalf("mode/backend = %q/%q, want CODER/acp", def.Mode, def.CoderBackend)
	}
	if def.ACPProxyID != "codex" {
		t.Fatalf("acpProxyId = %q, want codex", def.ACPProxyID)
	}
	if !AgentUsesACPCoderBackend(def) {
		t.Fatalf("expected ACP CODER backend")
	}
	if def.Project.Git.ExpectedBranch != "main" {
		t.Fatalf("expected branch = %q, want main", def.Project.Git.ExpectedBranch)
	}
}

func TestParseAgentFileRejectsACPCoderPromptFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: coder\n" +
		"mode: CODER\n" +
		"runtimeConfig:\n" +
		"  coderBackend: acp\n" +
		"  acpProxyId: codex\n" +
		"projectConfig:\n" +
		"  promptFiles:\n" +
		"    - AGENTS.md\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	_, err := parseAgentFile(path)
	if err == nil || !strings.Contains(err.Error(), "projectConfig.promptFiles is not supported") {
		t.Fatalf("expected ACP CODER promptFiles rejection, got %v", err)
	}
}

func TestParseAgentFileRejectsACPCoderProxyConfig(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: coder\n" +
		"mode: CODER\n" +
		"runtimeConfig:\n" +
		"  coderBackend: acp\n" +
		"  acpProxyId: codex\n" +
		"proxyConfig:\n" +
		"  baseUrl: http://127.0.0.1:3211\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	_, err := parseAgentFile(path)
	if err == nil || !strings.Contains(err.Error(), "proxyConfig is not supported") {
		t.Fatalf("expected ACP CODER proxyConfig rejection, got %v", err)
	}
}

func TestParseAgentFileRejectsACPCoderWithoutProxyID(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: coder\n" +
		"mode: CODER\n" +
		"runtimeConfig:\n" +
		"  coderBackend: acp\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	_, err := parseAgentFile(path)
	if err == nil || !strings.Contains(err.Error(), "runtimeConfig.acpProxyId is required") {
		t.Fatalf("expected ACP CODER acpProxyId rejection, got %v", err)
	}
}

func TestParseAgentFileInfersACPBackendFromProxyID(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "project")
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: coder\n" +
		"mode: CODER\n" +
		"runtimeConfig:\n" +
		"  acpProxyId: codex\n" +
		"  workspaceRoot: " + filepath.ToSlash(workspace) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if def.CoderBackend != AgentCoderBackendACP || def.ACPProxyID != "codex" {
		t.Fatalf("backend/proxy = %q/%q, want acp/codex", def.CoderBackend, def.ACPProxyID)
	}
	if !AgentUsesACPCoderBackend(def) {
		t.Fatalf("expected ACP CODER backend")
	}
}

func TestParseAgentFileAppliesCoderProfileDefaults(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "project")
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: coder\n" +
		"mode: CODER\n" +
		"runtimeConfig:\n" +
		"  workspaceRoot: " + filepath.ToSlash(workspace) + "\n"
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
	wantTags := []string{"system", "session"}
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
		"mode: CODER\n" +
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
		"runtimeConfig:\n" +
		"  workspaceRoot: " + filepath.ToSlash(workspace) + "\n"
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

func TestParseAgentFileAllowsCoderWithoutWorkspace(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	if err := os.WriteFile(path, []byte("key: coder\nmode: CODER\n"), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if def.Workspace.Root != "" {
		t.Fatalf("workspace root = %q, want empty runtime default", def.Workspace.Root)
	}
}

func TestParseAgentFileRejectsCoderRelativeWorkspace(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	if err := os.WriteFile(path, []byte("key: coder\nmode: CODER\nruntimeConfig:\n  workspaceRoot: ./project\n"), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	_, err := parseAgentFile(path)
	if err == nil || !strings.Contains(err.Error(), "runtimeConfig.workspaceRoot must be an absolute path") {
		t.Fatalf("expected absolute workspace requirement error, got %v", err)
	}
}

func TestParseAgentFileExpandsHomeWorkspaceRoot(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skipf("user home directory unavailable: %v", err)
	}

	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	if err := os.WriteFile(path, []byte("key: coder\nmode: CODER\nruntimeConfig:\n  workspaceRoot: ~/project\n"), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	want := filepath.Join(home, "project")
	if def.Workspace.Root != want {
		t.Fatalf("workspace root = %q, want %q", def.Workspace.Root, want)
	}
}

func TestParseAgentFileExpandsBareHomeWorkspaceRoot(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skipf("user home directory unavailable: %v", err)
	}

	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	if err := os.WriteFile(path, []byte("key: coder\nmode: CODER\nruntimeConfig:\n  workspaceRoot: \"~\"\n"), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if def.Workspace.Root != filepath.Clean(home) {
		t.Fatalf("workspace root = %q, want %q", def.Workspace.Root, filepath.Clean(home))
	}
}

func TestParseAgentFileRejectsOtherUserHomeWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	if err := os.WriteFile(path, []byte("key: coder\nmode: CODER\nruntimeConfig:\n  workspaceRoot: ~other/project\n"), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	_, err := parseAgentFile(path)
	if err == nil || !strings.Contains(err.Error(), "runtimeConfig.workspaceRoot must be an absolute path") {
		t.Fatalf("expected absolute workspace requirement error, got %v", err)
	}
}

func TestParseAgentFileAcceptsChatWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: chat-worker\n" +
		"mode: REACT\n" +
		"runtimeConfig:\n" +
		"  workspaceRoot: \"@chat\"\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if def.Workspace.Root != AgentWorkspaceRootChat {
		t.Fatalf("workspace root = %q, want %q", def.Workspace.Root, AgentWorkspaceRootChat)
	}
}

func TestParseAgentFileRuntimeWorkspaceRootSetsCoderWorkspace(t *testing.T) {
	root := t.TempDir()
	runtimeWorkspace := filepath.Join(root, "runtime-project")
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: coder\n" +
		"mode: CODER\n" +
		"runtimeConfig:\n" +
		"  workspaceRoot: " + filepath.ToSlash(runtimeWorkspace) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if def.Workspace.Root != filepath.Clean(runtimeWorkspace) {
		t.Fatalf("workspace root = %q, want runtime %q", def.Workspace.Root, filepath.Clean(runtimeWorkspace))
	}
}

func TestParseAgentFileLoadsProjectConfig(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "project")
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: coder\n" +
		"mode: CODER\n" +
		"runtimeConfig:\n" +
		"  workspaceRoot: " + filepath.ToSlash(workspace) + "\n" +
		"projectConfig:\n" +
		"  promptFiles:\n" +
		"    - AGENTS.md\n" +
		"    - agent:AGENTS.md\n" +
		"  git:\n" +
		"    expectedBranch: main\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if def.Workspace.Root != filepath.Clean(workspace) {
		t.Fatalf("workspace root = %q, want %q", def.Workspace.Root, filepath.Clean(workspace))
	}
	wantPromptFiles := []AgentProjectPromptFile{
		{Source: "workspace", Path: "AGENTS.md"},
		{Source: "agent", Path: "AGENTS.md"},
	}
	if !reflect.DeepEqual(def.Project.PromptFiles, wantPromptFiles) {
		t.Fatalf("project prompt files = %#v, want %#v", def.Project.PromptFiles, wantPromptFiles)
	}
	if def.Project.Git.ExpectedBranch != "main" {
		t.Fatalf("expected branch = %q, want main", def.Project.Git.ExpectedBranch)
	}
}

func TestParseAgentFileAllowsSandboxCoderWithoutHostWorkspace(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: coder\n" +
		"mode: CODER\n" +
		"runtimeConfig:\n" +
		"  environmentId: toolbox\n" +
		"  level: run\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if def.Workspace.Root != "" {
		t.Fatalf("workspace root = %q, want empty for sandbox coder", def.Workspace.Root)
	}
}

func TestParseAgentFileRejectsLegacyCoderType(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	if err := os.WriteFile(path, []byte("key: coder\ntype: CODER\n"), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	_, err := parseAgentFile(path)
	if err == nil || !strings.Contains(err.Error(), "use mode: CODER") {
		t.Fatalf("expected legacy CODER type migration error, got %v", err)
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
		"memory_write",
		"memory_read",
		"memory_search",
		"memory_update",
		"memory_forget",
		"memory_timeline",
		"memory_promote",
		"memory_consolidate",
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
	for _, tool := range []string{"memory_write", "memory_read", "memory_search"} {
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
	for _, tool := range []string{"memory_update", "memory_forget", "memory_timeline", "memory_promote"} {
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
	for _, tool := range []string{"memory_write", "memory_read", "memory_search"} {
		if containsString(def.Tools, tool) {
			t.Fatalf("expected %s to stay disabled, got %#v", tool, def.Tools)
		}
	}
}

func TestParseAgentFileWithPromptsLoadsPlanExecuteConventionFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: demo\n" +
		"name: Demo\n" +
		"mode: PLAN_EXECUTE\n" +
		"modelConfig:\n" +
		"  modelKey: demo-model\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}
	for name, body := range map[string]string{
		"AGENTS.plan.md":    "plan convention",
		"AGENTS.execute.md": "execute convention",
		"AGENTS.summary.md": "summary convention",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	def, err := parseAgentFileWithPrompts(path, root)
	if err != nil {
		t.Fatalf("parse agent file with prompts: %v", err)
	}
	if def.PlanPrompt != "plan convention" || def.ExecutePrompt != "execute convention" || def.SummaryPrompt != "summary convention" {
		t.Fatalf("unexpected stage prompts: plan=%q execute=%q summary=%q", def.PlanPrompt, def.ExecutePrompt, def.SummaryPrompt)
	}
}

func TestParseAgentFileWithPromptsStagePromptFileOverridesConvention(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: demo\n" +
		"name: Demo\n" +
		"mode: PLAN_EXECUTE\n" +
		"modelConfig:\n" +
		"  modelKey: demo-model\n" +
		"planExecute:\n" +
		"  plan:\n" +
		"    promptFile: custom-plan.md\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.plan.md"), []byte("plan convention"), 0o644); err != nil {
		t.Fatalf("write convention prompt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "custom-plan.md"), []byte("custom plan"), 0o644); err != nil {
		t.Fatalf("write custom prompt: %v", err)
	}

	def, err := parseAgentFileWithPrompts(path, root)
	if err != nil {
		t.Fatalf("parse agent file with prompts: %v", err)
	}
	if def.PlanPrompt != "custom plan" {
		t.Fatalf("plan prompt = %q, want custom override", def.PlanPrompt)
	}
}

func TestParseAgentFileWithPromptsPlanExecuteFallbackOrder(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: demo\n" +
		"name: Demo\n" +
		"mode: PLAN_EXECUTE\n" +
		"promptFile: shared.md\n" +
		"modelConfig:\n" +
		"  modelKey: demo-model\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.plan.md"), []byte("plan convention"), 0o644); err != nil {
		t.Fatalf("write plan convention: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "shared.md"), []byte("shared fallback"), 0o644); err != nil {
		t.Fatalf("write shared prompt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("agents fallback"), 0o644); err != nil {
		t.Fatalf("write agents prompt: %v", err)
	}

	def, err := parseAgentFileWithPrompts(path, root)
	if err != nil {
		t.Fatalf("parse agent file with prompts: %v", err)
	}
	if def.PlanPrompt != "plan convention" {
		t.Fatalf("plan prompt = %q, want convention", def.PlanPrompt)
	}
	if def.ExecutePrompt != "shared fallback" || def.SummaryPrompt != "shared fallback" {
		t.Fatalf("execute/summary prompts = %q/%q, want shared fallback", def.ExecutePrompt, def.SummaryPrompt)
	}

	if err := os.Remove(filepath.Join(root, "shared.md")); err != nil {
		t.Fatalf("remove shared prompt: %v", err)
	}
	def, err = parseAgentFileWithPrompts(path, root)
	if err != nil {
		t.Fatalf("parse agent file with prompts after fallback removal: %v", err)
	}
	if def.ExecutePrompt != "agents fallback" || def.SummaryPrompt != "agents fallback" {
		t.Fatalf("execute/summary prompts = %q/%q, want AGENTS.md fallback", def.ExecutePrompt, def.SummaryPrompt)
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
