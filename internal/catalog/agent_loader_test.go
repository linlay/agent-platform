package catalog

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

func parseAgentFileWithPromptsForTest(path string, agentDir string) (AgentDefinition, error) {
	def, tree, err := parseAgentFileRaw(path)
	if err != nil {
		return def, err
	}
	loadAgentPrompts(agentDir, &def, tree)
	return def, nil
}

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

func TestParseAgentFileSupportsNestedPlanExecuteStageConfig(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: nested-stage\n" +
		"name: Nested Stage\n" +
		"mode: PLAN_EXECUTE\n" +
		"modelConfig:\n" +
		"  modelKey: root-model\n" +
		"  sampling:\n" +
		"    temperature: 0.7\n" +
		"    topP: 0.9\n" +
		"stageSettings:\n" +
		"  execute:\n" +
		"    modelConfig:\n" +
		"      modelKey: nested-model\n" +
		"      reasoning:\n" +
		"        enabled: true\n" +
		"        effort: MEDIUM\n" +
		"      maxOutputTokens: 8192\n" +
		"      sampling:\n" +
		"        frequencyPenalty: 0.1\n" +
		"    toolConfig:\n" +
		"      tools:\n" +
		"        - bash\n" +
		"        - file_read\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	settings := contracts.ResolvePlanExecuteSettings(def.StageSettings, 0, 0)
	if settings.Execute.ModelKey != "nested-model" {
		t.Fatalf("expected nested execute model key, got %q", settings.Execute.ModelKey)
	}
	if settings.Execute.ReasoningEnabled != true || settings.Execute.ReasoningEffort != "MEDIUM" {
		t.Fatalf("expected nested reasoning settings, got enabled=%v effort=%q", settings.Execute.ReasoningEnabled, settings.Execute.ReasoningEffort)
	}
	if settings.Execute.MaxOutputTokens != 8192 {
		t.Fatalf("expected nested max output tokens, got %d", settings.Execute.MaxOutputTokens)
	}
	if !reflect.DeepEqual(settings.Execute.Tools, []string{"bash", "file_read"}) {
		t.Fatalf("expected nested tools to win, got %#v", settings.Execute.Tools)
	}
	if settings.Execute.Sampling.Temperature == nil || *settings.Execute.Sampling.Temperature != 0.7 {
		t.Fatalf("expected execute temperature inherited from root model sampling, got %#v", settings.Execute.Sampling)
	}
	if settings.Execute.Sampling.FrequencyPenalty == nil || *settings.Execute.Sampling.FrequencyPenalty != 0.1 {
		t.Fatalf("expected nested frequency penalty, got %#v", settings.Execute.Sampling)
	}
}

func TestParseAgentFileMergesStageSettingsBudgetIntoResolvedBudget(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: stage-budget\n" +
		"name: Stage Budget\n" +
		"mode: PLAN_EXECUTE\n" +
		"modelConfig:\n" +
		"  modelKey: demo-model\n" +
		"budget:\n" +
		"  maxSteps: 120\n" +
		"stageSettings:\n" +
		"  plan:\n" +
		"    budget:\n" +
		"      maxSteps: 20\n" +
		"      timeout: 600\n" +
		"      tool:\n" +
		"        timeout: 90\n" +
		"        maxCalls: 40\n" +
		"  summary:\n" +
		"    budget:\n" +
		"      maxSteps: 3\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	budget := contracts.ResolveBudget(config.Config{}, def.Budget)
	if budget.MaxSteps != 120 {
		t.Fatalf("root max steps = %d, want 120", budget.MaxSteps)
	}
	if budget.Stages["plan"].MaxSteps != 20 || budget.Stages["plan"].Tool.Timeout != 90 || budget.Stages["plan"].Tool.MaxCalls != 40 {
		t.Fatalf("plan stage budget = %#v, want maxSteps 20 tool timeout 90 maxCalls 40", budget.Stages["plan"])
	}
	if budget.Stages["summary"].MaxSteps != 3 {
		t.Fatalf("summary stage budget = %#v, want maxSteps 3", budget.Stages["summary"])
	}
	if stageBudget := mapNode(mapNode(def.Budget["stages"])["plan"]); stageBudget["timeout"] != nil {
		t.Fatalf("stage-level timeout should not be merged into effective budget, got %#v", stageBudget)
	}
}

func TestParseAgentFileReadsProxyTransport(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: proxy-demo\n" +
		"name: Proxy Demo\n" +
		"mode: PROXY\n" +
		"modelConfig:\n" +
		"  modelKey: mock-model\n" +
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
		"modelConfig:\n" +
		"  modelKey: mock-model\n" +
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
		"modelConfig:\n" +
		"  modelKey: mock-model\n" +
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

func TestParseAgentFileRejectsNonACPAgentsWithoutModelConfig(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
	}{
		{
			name: "react",
			lines: []string{
				"key: react-demo",
				"name: React Demo",
				"mode: REACT",
			},
		},
		{
			name: "plan-execute",
			lines: []string{
				"key: plan-demo",
				"name: Plan Demo",
				"mode: PLAN_EXECUTE",
			},
		},
		{
			name: "native-coder",
			lines: []string{
				"key: coder-demo",
				"name: Coder Demo",
				"mode: CODER",
			},
		},
		{
			name: "kbase",
			lines: []string{
				"key: kbase-demo",
				"name: KBase Demo",
				"mode: KBASE",
				"runtimeConfig:",
				"  workspaceRoot: " + filepath.ToSlash(t.TempDir()),
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "agent.yml")
			content := strings.Join(tc.lines, "\n") + "\n"
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatalf("write agent file: %v", err)
			}
			_, err := parseAgentFile(path)
			if err == nil || !strings.Contains(err.Error(), "modelConfig.modelKey is required") {
				t.Fatalf("expected modelConfig.modelKey error, got %v", err)
			}
		})
	}
}

func TestParseAgentFileAllowsProxyWithoutModelConfig(t *testing.T) {
	tests := []struct {
		name string
		mode string
	}{
		{name: "proxy", mode: "PROXY"},
		{name: "acp-proxy alias", mode: "ACP-PROXY"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "agent.yml")
			content := strings.Join([]string{
				"key: proxy-demo",
				"name: Proxy Demo",
				"mode: " + tc.mode,
				"proxyConfig:",
				"  baseUrl: http://127.0.0.1:3210",
			}, "\n") + "\n"
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatalf("write agent file: %v", err)
			}

			def, err := parseAgentFile(path)
			if err != nil {
				t.Fatalf("parse agent file: %v", err)
			}
			if def.Mode != AgentModeProxy || def.ModelKey != "" {
				t.Fatalf("unexpected proxy def mode=%q model=%q", def.Mode, def.ModelKey)
			}
			if def.ProxyConfig == nil || strings.TrimSpace(def.ProxyConfig.BaseURL) == "" {
				t.Fatalf("expected proxy config, got %#v", def.ProxyConfig)
			}
		})
	}
}

func TestParseAgentFileDefaultsModeAndVisibility(t *testing.T) {
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

func TestParseAgentFileReadsVisibility(t *testing.T) {
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
		"    - invoke\n"
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
}

func TestParseAgentFileRejectsRemovedToolConfigBuckets(t *testing.T) {
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

	if _, err := parseAgentFile(path); err == nil || !strings.Contains(err.Error(), "toolConfig.backends is no longer supported") {
		t.Fatalf("expected removed toolConfig bucket error, got %v", err)
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
		"modelConfig:\n" +
		"  modelKey: mock-model\n" +
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

func TestParseAgentFileSupportsACPCoderProxyID(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "project")
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: coder\n" +
		"mode: CODER\n" +
		"runtimeConfig:\n" +
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
	if def.Mode != AgentModeCoder {
		t.Fatalf("mode = %q, want CODER", def.Mode)
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

func TestParseAgentFileUsesACPBackendFromProxyID(t *testing.T) {
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
	if def.ACPProxyID != "codex" {
		t.Fatalf("acpProxyId = %q, want codex", def.ACPProxyID)
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
		"modelConfig:\n" +
		"  modelKey: mock-model\n" +
		"runtimeConfig:\n" +
		"  workspaceRoot: " + filepath.ToSlash(workspace) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	wantTools := []string{"bash", "file_read", "file_write", "file_edit", "file_glob", "file_grep", "datetime", "regex", "vision_recognize", "plan_add_tasks", "plan_get_tasks", "plan_update_task"}
	if !reflect.DeepEqual(def.Tools, wantTools) {
		t.Fatalf("tools = %#v, want %#v", def.Tools, wantTools)
	}
	wantTags := []string{"system", "session"}
	if !reflect.DeepEqual(def.ContextTags, wantTags) {
		t.Fatalf("context tags = %#v, want %#v", def.ContextTags, wantTags)
	}
	if got := intNode(def.Budget["timeout"]); got != 3600 {
		t.Fatalf("timeout = %d, want 3600", got)
	}
	if got := intNode(def.Budget["maxSteps"]); got != 240 {
		t.Fatalf("maxSteps = %d, want 240", got)
	}
	if got := intNode(mapNode(def.Budget["tool"])["maxCalls"]); got != 200 {
		t.Fatalf("tool.maxCalls = %d, want 200", got)
	}
	if def.Name != "" || def.Role != "" || def.Description != "coder" {
		t.Fatalf("identity defaults = name:%q role:%q description:%q, want name/role empty, description key fallback", def.Name, def.Role, def.Description)
	}
}

func TestParseAgentFileAllowsCoderProfileOverrides(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "project")
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: coder\n" +
		"mode: CODER\n" +
		"modelConfig:\n" +
		"  modelKey: mock-model\n" +
		"toolConfig:\n" +
		"  tools:\n" +
		"    - datetime\n" +
		"contextConfig:\n" +
		"  tags:\n" +
		"    - owner\n" +
		"budget:\n" +
		"  timeout: 1234\n" +
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
	if got := intNode(def.Budget["timeout"]); got != 1234 {
		t.Fatalf("timeout = %d, want explicit override", got)
	}
}

func TestParseAgentFileAllowsCoderWithoutWorkspace(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	if err := os.WriteFile(path, []byte("key: coder\nmode: CODER\nmodelConfig:\n  modelKey: mock-model\n"), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if def.Workspace.Root != "" {
		t.Fatalf("workspace root = %q, want empty runtime default", def.Workspace.Root)
	}
	if !containsString(def.Tools, "vision_recognize") {
		t.Fatalf("expected CODER default tools to include vision_recognize, got %#v", def.Tools)
	}
	for _, tool := range []string{"plan_add_tasks", "plan_get_tasks", "plan_update_task"} {
		if !containsString(def.Tools, tool) {
			t.Fatalf("expected CODER default tools to include %s, got %#v", tool, def.Tools)
		}
	}
	for _, tool := range []string{"memory_write", "memory_read", "memory_search"} {
		if containsString(def.Tools, tool) {
			t.Fatalf("expected CODER default tools not to include %s without memoryConfig, got %#v", tool, def.Tools)
		}
	}
}

func TestParseAgentFileKBaseDefaultsAndConfig(t *testing.T) {
	workspace := t.TempDir()
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: docs\n" +
		"mode: KBASE\n" +
		"modelConfig:\n" +
		"  modelKey: mock-model\n" +
		"runtimeConfig:\n" +
		"  workspaceRoot: " + filepath.ToSlash(workspace) + "\n" +
		"kbaseConfig:\n" +
		"  embedding:\n" +
		"    modelKey: openai-embedding\n" +
		"  storage:\n" +
		"    location: workspace\n" +
		"  include:\n" +
		"    - \"**/*.md\"\n" +
		"  chunk:\n" +
		"    maxChars: 2000\n" +
		"    overlapChars: 100\n" +
		"  retrieval:\n" +
		"    topK: 3\n" +
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
	if def.Mode != AgentModeKBase {
		t.Fatalf("mode = %q, want KBASE", def.Mode)
	}
	for _, tool := range []string{"kbase_search", "kbase_files", "kbase_read", "kbase_status", "kbase_refresh", "datetime"} {
		if !containsString(def.Tools, tool) {
			t.Fatalf("expected KBASE default tools to include %s, got %#v", tool, def.Tools)
		}
	}
	for _, tool := range []string{"bash", "file_read", "memory_search"} {
		if containsString(def.Tools, tool) {
			t.Fatalf("expected KBASE default tools not to include %s, got %#v", tool, def.Tools)
		}
	}
	if def.MemoryEnabled || def.MemoryConfig.Enabled {
		t.Fatalf("expected KBASE to ignore memoryConfig, got %#v", def.MemoryConfig)
	}
	if def.KBaseConfig.Embedding.ModelKey != "openai-embedding" || def.KBaseConfig.Storage.Location != "workspace" {
		t.Fatalf("unexpected kbase config: %#v", def.KBaseConfig)
	}
	if def.KBaseConfig.Chunk.MaxChars != 2000 || def.KBaseConfig.Chunk.OverlapChars != 100 {
		t.Fatalf("unexpected chunk config: %#v", def.KBaseConfig.Chunk)
	}
	if def.KBaseConfig.Retrieval.TopK != 3 {
		t.Fatalf("unexpected retrieval config: %#v", def.KBaseConfig.Retrieval)
	}
}

func TestParseAgentFileKBaseFiltersToolsAndStaticMemory(t *testing.T) {
	workspace := t.TempDir()
	agentDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(agentDir, "memory"), 0o755); err != nil {
		t.Fatalf("make memory dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "memory", "memory.md"), []byte("static memory"), 0o644); err != nil {
		t.Fatalf("write memory prompt: %v", err)
	}
	path := filepath.Join(agentDir, "agent.yml")
	content := "" +
		"key: docs\n" +
		"mode: KBASE\n" +
		"modelConfig:\n" +
		"  modelKey: mock-model\n" +
		"runtimeConfig:\n" +
		"  workspaceRoot: " + filepath.ToSlash(workspace) + "\n" +
		"toolConfig:\n" +
		"  tools:\n" +
		"    - kbase_search\n" +
		"    - kbase_files\n" +
		"    - memory_search\n" +
		"    - bash\n" +
		"    - datetime\n" +
		"kbaseConfig:\n" +
		"  embedding:\n" +
		"    modelKey: openai-embedding\n" +
		"memoryConfig:\n" +
		"  enabled: true\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFileWithPromptsForTest(path, agentDir)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if got, want := strings.Join(def.Tools, ","), "kbase_search,kbase_files,datetime"; got != want {
		t.Fatalf("unexpected KBASE filtered tools: got %q want %q", got, want)
	}
	if def.MemoryEnabled || def.MemoryConfig.Enabled || def.StaticMemoryPrompt != "" {
		t.Fatalf("expected KBASE memory to be ignored, enabled=%v config=%#v static=%q", def.MemoryEnabled, def.MemoryConfig, def.StaticMemoryPrompt)
	}
	for _, include := range []string{"**/*.html", "**/*.htm", "**/*.pdf", "**/*.docx", "**/*.pptx"} {
		if !containsString(def.KBaseConfig.Include, include) {
			t.Fatalf("expected KBASE default include to contain %s, got %#v", include, def.KBaseConfig.Include)
		}
	}
}

func TestParseAgentFileRejectsRemovedKBaseEmbeddingFields(t *testing.T) {
	workspace := t.TempDir()
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: docs\n" +
		"mode: KBASE\n" +
		"modelConfig:\n" +
		"  modelKey: mock-model\n" +
		"runtimeConfig:\n" +
		"  workspaceRoot: " + filepath.ToSlash(workspace) + "\n" +
		"kbaseConfig:\n" +
		"  embedding:\n" +
		"    providerKey: openai\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	if _, err := parseAgentFile(path); err == nil || !strings.Contains(err.Error(), "kbaseConfig.embedding.providerKey is no longer supported") {
		t.Fatalf("expected removed kbase embedding field error, got %v", err)
	}
}

func TestParseAgentFileRejectsKBaseWithoutWorkspace(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	if err := os.WriteFile(path, []byte("key: docs\nmode: KBASE\n"), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	_, err := parseAgentFile(path)
	if err == nil || !strings.Contains(err.Error(), "workspaceRoot is required") {
		t.Fatalf("expected KBASE workspace error, got %v", err)
	}
}

func TestParseAgentFileRejectsKBaseChatWorkspace(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	if err := os.WriteFile(path, []byte("key: docs\nmode: KBASE\nruntimeConfig:\n  workspaceRoot: @chat\n"), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	_, err := parseAgentFile(path)
	if err == nil || !strings.Contains(err.Error(), "not \"@chat\"") {
		t.Fatalf("expected KBASE @chat workspace rejection, got %v", err)
	}
}

func TestParseAgentFileRejectsKBaseWithoutModelConfig(t *testing.T) {
	workspace := t.TempDir()
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: docs\n" +
		"mode: KBASE\n" +
		"runtimeConfig:\n" +
		"  workspaceRoot: " + filepath.ToSlash(workspace) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	_, err := parseAgentFile(path)
	if err == nil || !strings.Contains(err.Error(), "modelConfig.modelKey is required") {
		t.Fatalf("expected KBASE model config error, got %v", err)
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
	if err := os.WriteFile(path, []byte("key: coder\nmode: CODER\nmodelConfig:\n  modelKey: mock-model\nruntimeConfig:\n  workspaceRoot: ~/project\n"), 0o644); err != nil {
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
	if err := os.WriteFile(path, []byte("key: coder\nmode: CODER\nmodelConfig:\n  modelKey: mock-model\nruntimeConfig:\n  workspaceRoot: \"~\"\n"), 0o644); err != nil {
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
		"modelConfig:\n" +
		"  modelKey: mock-model\n" +
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

func TestParseAgentFileAcceptsSlashWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: chat-worker\n" +
		"mode: REACT\n" +
		"modelConfig:\n" +
		"  modelKey: mock-model\n" +
		"runtimeConfig:\n" +
		"  workspaceRoot: \"/\"\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	want, err := filepath.Abs("/")
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	if def.Workspace.Root != want {
		t.Fatalf("workspace root = %q, want %q", def.Workspace.Root, want)
	}
}

func TestParseAgentFileReadsHostAccessAndSandboxMounts(t *testing.T) {
	root := t.TempDir()
	owner := filepath.Join(root, "owner")
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: bootstrap\n" +
		"mode: REACT\n" +
		"modelConfig:\n" +
		"  modelKey: mock-model\n" +
		"runtimeConfig:\n" +
		"  workspaceRoot: \"@chat\"\n" +
		"  hostAccess:\n" +
		"    readRoots:\n" +
		"      - \"@owner\"\n" +
		"      - " + filepath.ToSlash(owner) + "\n" +
		"    writeRoots:\n" +
		"      - \"@owner\"\n" +
		"  sandboxMounts:\n" +
		"    - platform: owner\n" +
		"      target: /owner\n" +
		"      mode: rw\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if !reflect.DeepEqual(def.HostAccess.ReadRoots, []string{"@owner", filepath.Clean(owner)}) {
		t.Fatalf("host read roots = %#v", def.HostAccess.ReadRoots)
	}
	if !reflect.DeepEqual(def.HostAccess.WriteRoots, []string{"@owner"}) {
		t.Fatalf("host write roots = %#v", def.HostAccess.WriteRoots)
	}
	mounts, _ := def.Runtime["sandboxMounts"].([]map[string]any)
	if len(mounts) != 1 || mounts[0]["platform"] != "owner" || mounts[0]["mode"] != "rw" {
		t.Fatalf("expected sandboxMounts to populate runtime sandboxMounts, got %#v", def.Runtime["sandboxMounts"])
	}
}

func TestParseAgentFileRuntimeWorkspaceRootSetsCoderWorkspace(t *testing.T) {
	root := t.TempDir()
	runtimeWorkspace := filepath.Join(root, "runtime-project")
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: coder\n" +
		"mode: CODER\n" +
		"modelConfig:\n" +
		"  modelKey: mock-model\n" +
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
		"modelConfig:\n" +
		"  modelKey: mock-model\n" +
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
		"modelConfig:\n" +
		"  modelKey: mock-model\n" +
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
		"    timeout: 15\n" +
		"  autoRemember:\n" +
		"    enabled: true\n" +
		"    modelKey: minimax-m2_7-anthropic\n" +
		"    timeout: 60\n"
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
		def.MemoryConfig.Embedding.Timeout != 15 {
		t.Fatalf("unexpected embedding config: %#v", def.MemoryConfig.Embedding)
	}
	if !def.MemoryConfig.AutoRemember.Enabled ||
		def.MemoryConfig.AutoRemember.ModelKey != "minimax-m2_7-anthropic" ||
		def.MemoryConfig.AutoRemember.Timeout != 60 {
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

	def, err := parseAgentFileWithPromptsForTest(path, root)
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
		"stageSettings:\n" +
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

	def, err := parseAgentFileWithPromptsForTest(path, root)
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

	def, err := parseAgentFileWithPromptsForTest(path, root)
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
	def, err = parseAgentFileWithPromptsForTest(path, root)
	if err != nil {
		t.Fatalf("parse agent file with prompts after fallback removal: %v", err)
	}
	if def.ExecutePrompt != "agents fallback" || def.SummaryPrompt != "agents fallback" {
		t.Fatalf("execute/summary prompts = %q/%q, want AGENTS.md fallback", def.ExecutePrompt, def.SummaryPrompt)
	}
}

func TestParseAgentFileWithPromptsLoadsSoulSections(t *testing.T) {
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
	if err := os.WriteFile(soulPath, []byte("# Identity\n\n- key: demo\n\n## Mission\n\nCurrent mission"), 0o644); err != nil {
		t.Fatalf("write soul file: %v", err)
	}

	def, err := parseAgentFileWithPromptsForTest(path, root)
	if err != nil {
		t.Fatalf("parse agent file with prompts: %v", err)
	}
	if !strings.Contains(def.SoulPrompt, "Current mission") {
		t.Fatalf("expected soul prompt to load, got %q", def.SoulPrompt)
	}
	if !strings.Contains(def.SoulPrompt, "# Identity") || !strings.Contains(def.SoulPrompt, "## Mission") {
		t.Fatalf("expected headings to remain in soul prompt, got %q", def.SoulPrompt)
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

	def, err := parseAgentFileWithPromptsForTest(path, root)
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
