package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

func TestResolveSandboxPathsLocalModeDisabledHub(t *testing.T) {
	t.Parallel()

	cfg := testPromptContextConfig(t)
	cfg.ContainerHub.Enabled = false
	def := testPromptContextDefinition(cfg.Paths)

	paths := resolveSandboxPaths(cfg, def, "chat-1")
	if paths.WorkspaceDir != absTestPath(t, filepath.Join(cfg.Paths.ChatsDir, "chat-1")) {
		t.Fatalf("workspace dir = %q", paths.WorkspaceDir)
	}
	if paths.RootDir != absTestPath(t, cfg.Paths.RootDir) {
		t.Fatalf("root dir = %q", paths.RootDir)
	}
	if paths.AgentDir != absTestPath(t, def.AgentDir) {
		t.Fatalf("agent dir = %q", paths.AgentDir)
	}
	if paths.OwnerDir != absTestPath(t, cfg.Paths.OwnerDir) {
		t.Fatalf("owner dir = %q", paths.OwnerDir)
	}
	if paths.MemoryDir != absTestPath(t, cfg.Paths.MemoryDir) {
		t.Fatalf("memory dir = %q", paths.MemoryDir)
	}
	if paths.SkillsDir != absTestPath(t, filepath.Join(def.AgentDir, "skills")) {
		t.Fatalf("skills dir = %q", paths.SkillsDir)
	}
}

func TestResolveSandboxPathsLocalModeLocalEngine(t *testing.T) {
	t.Parallel()

	cfg := testPromptContextConfig(t)
	cfg.ContainerHub.Enabled = true
	cfg.ContainerHub.ResolvedEngine = "local"
	def := testPromptContextDefinition(cfg.Paths)

	paths := resolveSandboxPaths(cfg, def, "chat-1")
	if paths.WorkspaceDir != absTestPath(t, filepath.Join(cfg.Paths.ChatsDir, "chat-1")) {
		t.Fatalf("workspace dir = %q", paths.WorkspaceDir)
	}
	if paths.SkillsMarketDir != absTestPath(t, cfg.Paths.SkillsMarketDir) {
		t.Fatalf("skills market dir = %q", paths.SkillsMarketDir)
	}
	if paths.AgentsDir != absTestPath(t, cfg.Paths.AgentsDir) {
		t.Fatalf("agents dir = %q", paths.AgentsDir)
	}
	if paths.ModelsDir != absTestPath(t, filepath.Join(cfg.Paths.RegistriesDir, "models")) {
		t.Fatalf("models dir = %q", paths.ModelsDir)
	}
	if paths.ViewportServersDir != absTestPath(t, filepath.Join(cfg.Paths.RegistriesDir, "viewport-servers")) {
		t.Fatalf("viewport servers dir = %q", paths.ViewportServersDir)
	}
	if paths.ViewportsDir != absTestPath(t, filepath.Join(filepath.Dir(filepath.Clean(cfg.Paths.RegistriesDir)), "viewports")) {
		t.Fatalf("viewports dir = %q", paths.ViewportsDir)
	}
}

func TestResolveSandboxPathsContainerMode(t *testing.T) {
	t.Parallel()

	cfg := testPromptContextConfig(t)
	cfg.ContainerHub.Enabled = true
	cfg.ContainerHub.ResolvedEngine = "docker"
	def := testPromptContextDefinition(cfg.Paths)

	paths := resolveSandboxPaths(cfg, def, "chat-1")
	if paths.WorkspaceDir != "/workspace" {
		t.Fatalf("workspace dir = %q", paths.WorkspaceDir)
	}
	if paths.AgentDir != "/agent" {
		t.Fatalf("agent dir = %q", paths.AgentDir)
	}
	if paths.OwnerDir != "/owner" {
		t.Fatalf("owner dir = %q", paths.OwnerDir)
	}
	if paths.MemoryDir != "/memory" {
		t.Fatalf("memory dir = %q", paths.MemoryDir)
	}
}

func TestResolveLocalPathsIncludesAgentAndRegistryPaths(t *testing.T) {
	t.Parallel()

	cfg := testPromptContextConfig(t)
	agentDir := filepath.Join(cfg.Paths.AgentsDir, "demo-agent")
	chatDir := filepath.Join(cfg.Paths.ChatsDir, "chat-1")
	if err := os.MkdirAll(chatDir, 0o755); err != nil {
		t.Fatalf("create chat attachments dir: %v", err)
	}

	paths, err := resolveLocalPaths(cfg.Paths, "chat-1", agentDir, "")
	if err != nil {
		t.Fatalf("resolveLocalPaths() error = %v", err)
	}
	if paths.AgentDir != agentDir {
		t.Fatalf("agent dir = %q", paths.AgentDir)
	}
	if paths.SkillsDir != filepath.Join(agentDir, "skills") {
		t.Fatalf("skills dir = %q", paths.SkillsDir)
	}
	if paths.SkillsMarketDir != "" {
		t.Fatalf("expected no default skills market dir, got %q", paths.SkillsMarketDir)
	}
	if paths.TeamsDir != cfg.Paths.TeamsDir {
		t.Fatalf("teams dir = %q", paths.TeamsDir)
	}
	if paths.ModelsDir != filepath.Join(cfg.Paths.RegistriesDir, "models") {
		t.Fatalf("models dir = %q", paths.ModelsDir)
	}
	if paths.ProvidersDir != filepath.Join(cfg.Paths.RegistriesDir, "providers") {
		t.Fatalf("providers dir = %q", paths.ProvidersDir)
	}
	if paths.MCPServersDir != filepath.Join(cfg.Paths.RegistriesDir, "mcp-servers") {
		t.Fatalf("mcp servers dir = %q", paths.MCPServersDir)
	}
	if paths.ViewportServersDir != filepath.Join(cfg.Paths.RegistriesDir, "viewport-servers") {
		t.Fatalf("viewport servers dir = %q", paths.ViewportServersDir)
	}
	if paths.ToolsDir != cfg.Paths.ToolsDir {
		t.Fatalf("tools dir = %q", paths.ToolsDir)
	}
	if paths.ViewportsDir != filepath.Join(filepath.Dir(filepath.Clean(cfg.Paths.RegistriesDir)), "viewports") {
		t.Fatalf("viewports dir = %q", paths.ViewportsDir)
	}
	if paths.ChatAttachmentsDir != filepath.Join(cfg.Paths.ChatsDir, "chat-1") {
		t.Fatalf("chat attachments dir = %q", paths.ChatAttachmentsDir)
	}
	if paths.WorkingDirectory == "" {
		t.Fatal("expected working directory to be populated")
	}
}

func TestResolveLocalPathsResolvesChatWorkspaceRoot(t *testing.T) {
	t.Parallel()

	cfg := testPromptContextConfig(t)
	paths, err := resolveLocalPaths(cfg.Paths, "chat-1", "", catalog.AgentWorkspaceRootChat)
	if err != nil {
		t.Fatalf("resolveLocalPaths() error = %v", err)
	}
	want := absTestPath(t, filepath.Join(cfg.Paths.ChatsDir, "chat-1"))
	if paths.WorkspaceDir != want {
		t.Fatalf("workspace dir = %q, want %q", paths.WorkspaceDir, want)
	}
	if paths.WorkingDirectory != want {
		t.Fatalf("working dir = %q, want %q", paths.WorkingDirectory, want)
	}
}

func TestResolveLocalPathsCreatesChatAttachmentsDir(t *testing.T) {
	t.Parallel()

	cfg := testPromptContextConfig(t)
	paths, err := resolveLocalPaths(cfg.Paths, "chat-missing", "", "")
	if err != nil {
		t.Fatalf("resolveLocalPaths() error = %v", err)
	}
	want := absTestPath(t, filepath.Join(cfg.Paths.ChatsDir, "chat-missing"))
	if paths.ChatAttachmentsDir != want {
		t.Fatalf("chat attachments dir = %q, want %q", paths.ChatAttachmentsDir, want)
	}
	if stat, err := os.Stat(want); err != nil || !stat.IsDir() {
		t.Fatalf("expected chat attachments dir to be created, stat=%#v err=%v", stat, err)
	}
	if paths.WorkingDirectory == "" {
		t.Fatal("expected working directory to be populated")
	}
}

func TestResolveLocalWorkspaceDirUsesChatDirWhenChatIDProvided(t *testing.T) {
	t.Parallel()

	cfg := testPromptContextConfig(t)

	workspaceDir := resolveLocalWorkspaceDir(cfg.Paths, "chat-1")
	if workspaceDir != absTestPath(t, filepath.Join(cfg.Paths.ChatsDir, "chat-1")) {
		t.Fatalf("workspace dir = %q", workspaceDir)
	}
}

func TestBuildRuntimeContextDefaultsHostWorkspaceToChatDir(t *testing.T) {
	t.Parallel()

	cfg := testPromptContextConfig(t)
	s := &Server{
		deps: Dependencies{
			Config:   cfg,
			Registry: testCatalogRegistry{},
		},
	}

	context, err := s.buildRuntimeRequestContext(runtimeRequestContextInput{
		agentKey: "demo-agent",
		chatID:   "chat-default",
		definition: catalog.AgentDefinition{
			Key:      "demo-agent",
			Mode:     "REACT",
			AgentDir: filepath.Join(cfg.Paths.AgentsDir, "demo-agent"),
		},
	})
	if err != nil {
		t.Fatalf("buildRuntimeRequestContext() error = %v", err)
	}
	want := absTestPath(t, filepath.Join(cfg.Paths.ChatsDir, "chat-default"))
	if context.LocalPaths.WorkspaceDir != want {
		t.Fatalf("workspace dir = %q, want %q", context.LocalPaths.WorkspaceDir, want)
	}
	if context.LocalPaths.ChatAttachmentsDir != want {
		t.Fatalf("chat attachments dir = %q, want %q", context.LocalPaths.ChatAttachmentsDir, want)
	}
	if context.LocalPaths.WorkingDirectory != want {
		t.Fatalf("working dir = %q, want %q", context.LocalPaths.WorkingDirectory, want)
	}
}

func TestBuildRuntimeContextKeepsExplicitWorkspaceAndChatDir(t *testing.T) {
	t.Parallel()

	cfg := testPromptContextConfig(t)
	s := &Server{
		deps: Dependencies{
			Config:   cfg,
			Registry: testCatalogRegistry{},
		},
	}

	context, err := s.buildRuntimeRequestContext(runtimeRequestContextInput{
		agentKey: "admin-agent",
		chatID:   "chat-admin",
		definition: catalog.AgentDefinition{
			Key:      "admin-agent",
			Mode:     "REACT",
			AgentDir: filepath.Join(cfg.Paths.AgentsDir, "admin-agent"),
			Workspace: catalog.AgentWorkspaceConfig{
				Root: "/",
			},
		},
	})
	if err != nil {
		t.Fatalf("buildRuntimeRequestContext() error = %v", err)
	}
	wantChatDir := absTestPath(t, filepath.Join(cfg.Paths.ChatsDir, "chat-admin"))
	if context.LocalPaths.WorkspaceDir != "/" {
		t.Fatalf("workspace dir = %q, want /", context.LocalPaths.WorkspaceDir)
	}
	if context.LocalPaths.ChatAttachmentsDir != wantChatDir {
		t.Fatalf("chat attachments dir = %q, want %q", context.LocalPaths.ChatAttachmentsDir, wantChatDir)
	}
	if context.LocalPaths.WorkingDirectory != "/" {
		t.Fatalf("working dir = %q, want /", context.LocalPaths.WorkingDirectory)
	}
}

func TestBuildRuntimeContextSkipsSandboxContextWhenHubDisabled(t *testing.T) {
	t.Parallel()

	cfg := testPromptContextConfig(t)
	cfg.ContainerHub.Enabled = false
	s := &Server{
		deps: Dependencies{
			Config:   cfg,
			Registry: testCatalogRegistry{},
		},
	}

	context, err := s.buildRuntimeRequestContext(runtimeRequestContextInput{
		agentKey: "demo-agent",
		teamID:   "team-1",
		role:     "assistant",
		chatID:   "chat-1",
		chatName: "Chat 1",
		scene:    &api.Scene{URL: "https://example.com"},
		definition: catalog.AgentDefinition{
			Key:      "demo-agent",
			AgentDir: filepath.Join(cfg.Paths.AgentsDir, "demo-agent"),
			Runtime: map[string]any{
				"environmentId": "shell",
			},
		},
	})
	if err != nil {
		t.Fatalf("buildRuntimeRequestContext() error = %v", err)
	}
	if !context.LocalMode {
		t.Fatal("expected local mode when container hub is disabled")
	}
	if context.SandboxContext != nil {
		t.Fatalf("expected sandbox context to be skipped, got %#v", context.SandboxContext)
	}
}

func TestBuildRuntimeContextIncludesSandboxContextWhenSandboxConfigured(t *testing.T) {
	t.Parallel()

	cfg := testPromptContextConfig(t)
	cfg.ContainerHub.BaseURL = "://bad-url"
	s := &Server{
		deps: Dependencies{
			Config:   cfg,
			Registry: testCatalogRegistry{},
		},
	}

	_, err := s.buildRuntimeRequestContext(runtimeRequestContextInput{
		agentKey: "demo-agent",
		teamID:   "team-1",
		role:     "assistant",
		chatID:   "chat-1",
		chatName: "Chat 1",
		definition: catalog.AgentDefinition{
			Key:      "demo-agent",
			AgentDir: filepath.Join(cfg.Paths.AgentsDir, "demo-agent"),
			Runtime: map[string]any{
				"environmentId": "browser",
				"level":         "run",
			},
		},
	})
	if err == nil {
		t.Fatal("expected sandbox-configured agent to attempt sandbox context loading")
	}
	if !strings.Contains(err.Error(), `sandbox context failed to load environment prompt for "browser"`) {
		t.Fatalf("expected sandbox context load error, got %v", err)
	}
}

func TestBuildRuntimeContextIgnoresLegacySandboxTagWithoutSandboxConfig(t *testing.T) {
	t.Parallel()

	cfg := testPromptContextConfig(t)
	s := &Server{
		deps: Dependencies{
			Config:   cfg,
			Registry: testCatalogRegistry{},
		},
	}

	context, err := s.buildRuntimeRequestContext(runtimeRequestContextInput{
		agentKey: "demo-agent",
		teamID:   "team-1",
		role:     "assistant",
		chatID:   "chat-1",
		chatName: "Chat 1",
		definition: catalog.AgentDefinition{
			Key:         "demo-agent",
			AgentDir:    filepath.Join(cfg.Paths.AgentsDir, "demo-agent"),
			ContextTags: []string{"sandbox"},
		},
	})
	if err != nil {
		t.Fatalf("buildRuntimeRequestContext() error = %v", err)
	}
	if context.SandboxContext != nil {
		t.Fatalf("expected sandbox tag to have no effect, got %#v", context.SandboxContext)
	}
}

func TestBuildRuntimeContextKeepsLocalPathsWithoutSandboxConfigInContainerMode(t *testing.T) {
	t.Parallel()

	cfg := testPromptContextConfig(t)
	cfg.ContainerHub.Enabled = true
	cfg.ContainerHub.ResolvedEngine = "docker"
	s := &Server{
		deps: Dependencies{
			Config:   cfg,
			Registry: testCatalogRegistry{},
		},
	}

	agentDir := filepath.Join(cfg.Paths.AgentsDir, "demo-agent")
	context, err := s.buildRuntimeRequestContext(runtimeRequestContextInput{
		agentKey: "demo-agent",
		teamID:   "team-1",
		role:     "assistant",
		chatID:   "chat-1",
		chatName: "Chat 1",
		definition: catalog.AgentDefinition{
			Key:      "demo-agent",
			AgentDir: agentDir,
		},
	})
	if err != nil {
		t.Fatalf("buildRuntimeRequestContext() error = %v", err)
	}
	if context.LocalMode {
		t.Fatal("expected container mode to remain non-local")
	}
	if context.SandboxContext != nil {
		t.Fatalf("expected no sandbox context, got %#v", context.SandboxContext)
	}
	if context.LocalPaths.AgentDir != agentDir {
		t.Fatalf("local agent dir = %q", context.LocalPaths.AgentDir)
	}
	if context.LocalPaths.SkillsDir != filepath.Join(agentDir, "skills") {
		t.Fatalf("local skills dir = %q", context.LocalPaths.SkillsDir)
	}
	if context.SandboxPaths.WorkspaceDir != "/workspace" {
		t.Fatalf("sandbox workspace dir = %q", context.SandboxPaths.WorkspaceDir)
	}
	if context.LocalPaths.SkillsMarketDir != "" {
		t.Fatalf("expected local skills market dir to be omitted by default, got %q", context.LocalPaths.SkillsMarketDir)
	}
}

func TestBuildRuntimeContextIncludesSkillsMarketOnlyWithExplicitMount(t *testing.T) {
	t.Parallel()

	cfg := testPromptContextConfig(t)
	cfg.ContainerHub.Enabled = true
	cfg.ContainerHub.ResolvedEngine = "docker"
	s := &Server{
		deps: Dependencies{
			Config:   cfg,
			Registry: testCatalogRegistry{},
		},
	}

	context, err := s.buildRuntimeRequestContext(runtimeRequestContextInput{
		agentKey: "demo-agent",
		teamID:   "team-1",
		role:     "assistant",
		chatID:   "chat-1",
		chatName: "Chat 1",
		definition: catalog.AgentDefinition{
			Key:      "demo-agent",
			AgentDir: filepath.Join(cfg.Paths.AgentsDir, "demo-agent"),
			Runtime: map[string]any{
				"sandboxMounts": []map[string]any{
					{"platform": "skills-market", "mode": "ro"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("buildRuntimeRequestContext() error = %v", err)
	}
	if context.LocalPaths.SkillsMarketDir != cfg.Paths.SkillsMarketDir {
		t.Fatalf("local skills market dir = %q", context.LocalPaths.SkillsMarketDir)
	}
	if context.SandboxPaths.SkillsMarketDir != "/skills-market" {
		t.Fatalf("sandbox skills market dir = %q", context.SandboxPaths.SkillsMarketDir)
	}
}

func TestBuildRuntimeContextBackfillsSandboxReferencePaths(t *testing.T) {
	t.Parallel()

	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hasPrompt":false}`))
	}))
	defer hub.Close()

	cfg := testPromptContextConfig(t)
	cfg.ContainerHub.Enabled = true
	cfg.ContainerHub.ResolvedEngine = "docker"
	cfg.ContainerHub.BaseURL = hub.URL
	cfg.ContainerHub.RequestTimeout = 1000
	s := &Server{
		deps: Dependencies{
			Config:   cfg,
			Registry: testCatalogRegistry{},
		},
	}

	context, err := s.buildRuntimeRequestContext(runtimeRequestContextInput{
		agentKey: "demo-agent",
		teamID:   "team-1",
		chatID:   "chat-1",
		references: []api.Reference{
			{ID: "ref-name", Name: "report.docx"},
			{ID: "ref-url", URL: "/api/resource?file=chat-1%2Ffrom-url.docx"},
		},
		definition: catalog.AgentDefinition{
			Key:      "demo-agent",
			AgentDir: filepath.Join(cfg.Paths.AgentsDir, "demo-agent"),
			Runtime: map[string]any{
				"environmentId": "shell",
				"level":         "run",
			},
		},
	})
	if err != nil {
		t.Fatalf("buildRuntimeRequestContext() error = %v", err)
	}
	if got := context.References[0].SandboxPath; got != "/workspace/report.docx" {
		t.Fatalf("sandbox path from name = %q", got)
	}
	if got := context.References[1].SandboxPath; got != "/workspace/from-url.docx" {
		t.Fatalf("sandbox path from URL = %q", got)
	}
}

func TestBuildSkillCatalogPromptPrefersAgentLocalSkillAndParsesFrontMatter(t *testing.T) {
	t.Parallel()

	agentDir := t.TempDir()
	marketDir := t.TempDir()
	localSkillDir := filepath.Join(agentDir, "skills", "demo")
	marketSkillDir := filepath.Join(marketDir, "demo")
	if err := os.MkdirAll(localSkillDir, 0o755); err != nil {
		t.Fatalf("mkdir local skill: %v", err)
	}
	if err := os.MkdirAll(marketSkillDir, 0o755); err != nil {
		t.Fatalf("mkdir market skill: %v", err)
	}
	localSkill := strings.Join([]string{
		"---",
		`name: "Local Skill"`,
		`description: "Local description"`,
		"---",
		"",
		"# Ignored Heading",
		"",
		"body",
	}, "\n")
	if err := os.WriteFile(filepath.Join(localSkillDir, "SKILL.md"), []byte(localSkill), 0o644); err != nil {
		t.Fatalf("write local skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marketSkillDir, "SKILL.md"), []byte("# Market Skill\n\nMarket description"), 0o644); err != nil {
		t.Fatalf("write market skill: %v", err)
	}

	prompt := buildSkillCatalogPrompt(catalog.AgentDefinition{
		AgentDir: agentDir,
		Skills:   []string{"demo"},
	}, marketDir, contracts.DefaultPromptAppendConfig())

	if !strings.Contains(prompt, "skillId: demo") {
		t.Fatalf("expected skill block, got %q", prompt)
	}
	if !strings.Contains(prompt, "name: Local Skill") {
		t.Fatalf("expected local front matter name, got %q", prompt)
	}
	if !strings.Contains(prompt, "description: Local description") {
		t.Fatalf("expected local front matter description, got %q", prompt)
	}
	if strings.Contains(prompt, `name: name: "Local Skill"`) {
		t.Fatalf("expected front matter to be parsed, got %q", prompt)
	}
	if strings.Contains(prompt, "Market Skill") {
		t.Fatalf("expected local skill to win over market fallback, got %q", prompt)
	}
}

func TestBuildPromptAppendConfigUsesGlobalSkillInstructionsPrompt(t *testing.T) {
	t.Parallel()

	appendConfig := buildPromptAppendConfig(config.PromptsConfig{
		Skill: config.PromptSkillConfig{
			InstructionsPrompt: "global skill instructions",
		},
	}, catalog.AgentDefinition{})
	if appendConfig.Skill.InstructionsPrompt != "global skill instructions" {
		t.Fatalf("expected global instructions prompt override, got %q", appendConfig.Skill.InstructionsPrompt)
	}
}

func TestBuildPromptAppendConfigUsesGlobalSkillCatalogHeader(t *testing.T) {
	t.Parallel()

	appendConfig := buildPromptAppendConfig(config.PromptsConfig{
		Skill: config.PromptSkillConfig{
			CatalogHeader: "global skills header",
		},
	}, catalog.AgentDefinition{})
	if appendConfig.Skill.CatalogHeader != "global skills header" {
		t.Fatalf("expected global catalog header override, got %q", appendConfig.Skill.CatalogHeader)
	}
}

func TestBuildPromptAppendConfigUsesGlobalAppendixFields(t *testing.T) {
	t.Parallel()

	appendConfig := buildPromptAppendConfig(config.PromptsConfig{
		Skill: config.PromptSkillConfig{
			DisclosureHeader:  "global disclosure",
			InstructionsLabel: "global label",
		},
		ToolAppendix: config.ToolAppendixPromptsConfig{
			ToolDescriptionTitle: "global tool title",
			AfterCallHintTitle:   "global hint title",
		},
	}, catalog.AgentDefinition{})
	if appendConfig.Skill.DisclosureHeader != "global disclosure" {
		t.Fatalf("expected global disclosure header override, got %q", appendConfig.Skill.DisclosureHeader)
	}
	if appendConfig.Skill.InstructionsLabel != "global label" {
		t.Fatalf("expected global instructions label override, got %q", appendConfig.Skill.InstructionsLabel)
	}
	if appendConfig.Tool.ToolDescriptionTitle != "global tool title" {
		t.Fatalf("expected global tool title override, got %q", appendConfig.Tool.ToolDescriptionTitle)
	}
	if appendConfig.Tool.AfterCallHintTitle != "global hint title" {
		t.Fatalf("expected global hint title override, got %q", appendConfig.Tool.AfterCallHintTitle)
	}
}

func TestBuildPromptAppendConfigAgentRuntimePromptsOverrideGlobal(t *testing.T) {
	t.Parallel()

	appendConfig := buildPromptAppendConfig(config.PromptsConfig{
		Skill: config.PromptSkillConfig{
			CatalogHeader:      "global catalog",
			DisclosureHeader:   "global disclosure",
			InstructionsLabel:  "global label",
			InstructionsPrompt: "global instructions",
		},
		ToolAppendix: config.ToolAppendixPromptsConfig{
			ToolDescriptionTitle: "global tool title",
			AfterCallHintTitle:   "global hint title",
		},
	}, catalog.AgentDefinition{
		RuntimePrompts: catalog.AgentRuntimePrompts{
			Skill: catalog.SkillPromptConfig{
				CatalogHeader:     "agent catalog",
				DisclosureHeader:  "agent disclosure",
				InstructionsLabel: "agent label",
			},
			ToolAppendix: catalog.ToolAppendixPromptConfig{
				ToolDescriptionTitle: "agent tool title",
				AfterCallHintTitle:   "agent hint title",
			},
		},
	})
	if appendConfig.Skill.InstructionsPrompt != "global instructions" {
		t.Fatalf("expected global instructions to remain, got %q", appendConfig.Skill.InstructionsPrompt)
	}
	if appendConfig.Skill.CatalogHeader != "agent catalog" || appendConfig.Skill.DisclosureHeader != "agent disclosure" || appendConfig.Skill.InstructionsLabel != "agent label" {
		t.Fatalf("expected agent skill prompts to override global, got %#v", appendConfig.Skill)
	}
	if appendConfig.Tool.ToolDescriptionTitle != "agent tool title" || appendConfig.Tool.AfterCallHintTitle != "agent hint title" {
		t.Fatalf("expected agent tool prompts to override global, got %#v", appendConfig.Tool)
	}
}

func TestBuildSkillCatalogPromptPrependsInstructionsBeforeCatalogHeader(t *testing.T) {
	t.Parallel()

	agentDir := t.TempDir()
	marketDir := t.TempDir()
	skillDir := filepath.Join(marketDir, "demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Demo Skill\n\nDemo description"), 0o644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}

	appendConfig := contracts.DefaultPromptAppendConfig()
	appendConfig.Skill.InstructionsPrompt = "global skill instructions"
	appendConfig.Skill.InstructionsLabel = "instructions"
	appendConfig.Skill.CatalogHeader = "skills header"

	prompt := buildSkillCatalogPrompt(catalog.AgentDefinition{
		AgentDir: agentDir,
		Skills:   []string{"demo"},
	}, marketDir, appendConfig)

	labelIdx := strings.Index(prompt, "Skill instructions:\n")
	instructionsIdx := strings.Index(prompt, "global skill instructions")
	headerIdx := strings.Index(prompt, "skills header")
	skillIdx := strings.Index(prompt, "skillId: demo")
	if labelIdx < 0 || instructionsIdx < 0 || headerIdx < 0 || skillIdx < 0 {
		t.Fatalf("expected label, instructions, header, and skill block in prompt, got %q", prompt)
	}
	if !(labelIdx < instructionsIdx && instructionsIdx < headerIdx && headerIdx < skillIdx) {
		t.Fatalf("expected labeled instructions before header before skill block, got %q", prompt)
	}
}

func TestBuildSkillCatalogPromptLeavesInstructionsUnlabeledWhenLabelEmpty(t *testing.T) {
	t.Parallel()

	agentDir := t.TempDir()
	marketDir := t.TempDir()
	skillDir := filepath.Join(marketDir, "demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Demo Skill\n\nDemo description"), 0o644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}

	appendConfig := contracts.DefaultPromptAppendConfig()
	appendConfig.Skill.InstructionsPrompt = "global skill instructions"
	appendConfig.Skill.InstructionsLabel = ""
	appendConfig.Skill.CatalogHeader = "skills header"

	prompt := buildSkillCatalogPrompt(catalog.AgentDefinition{
		AgentDir: agentDir,
		Skills:   []string{"demo"},
	}, marketDir, appendConfig)

	if !strings.Contains(prompt, "global skill instructions") {
		t.Fatalf("expected instructions in prompt, got %q", prompt)
	}
	if strings.Contains(prompt, "Skill instructions:\n") {
		t.Fatalf("expected prompt to omit label prefix when label is empty, got %q", prompt)
	}
}

func TestBuildSkillCatalogPromptReturnsEmptyWhenNoSkillsResolve(t *testing.T) {
	t.Parallel()

	appendConfig := contracts.DefaultPromptAppendConfig()
	appendConfig.Skill.InstructionsPrompt = "global skill instructions"

	prompt := buildSkillCatalogPrompt(catalog.AgentDefinition{
		AgentDir: t.TempDir(),
		Skills:   []string{"missing"},
	}, t.TempDir(), appendConfig)
	if prompt != "" {
		t.Fatalf("expected empty prompt when no skills resolve, got %q", prompt)
	}
}

func testPromptContextConfig(t *testing.T) config.Config {
	t.Helper()

	root := t.TempDir()
	cfg := config.Config{
		Paths: config.PathsConfig{
			RegistriesDir:   filepath.Join(root, "runtime", "registries"),
			ToolsDir:        filepath.Join(root, "runtime", "tools"),
			OwnerDir:        filepath.Join(root, "runtime", "owner"),
			AgentsDir:       filepath.Join(root, "runtime", "agents"),
			TeamsDir:        filepath.Join(root, "runtime", "teams"),
			RootDir:         filepath.Join(root, "runtime", "root"),
			AutomationsDir:  filepath.Join(root, "runtime", "automations"),
			ChatsDir:        filepath.Join(root, "runtime", "chats"),
			MemoryDir:       filepath.Join(root, "runtime", "memory"),
			PanDir:          filepath.Join(root, "runtime", "pan"),
			SkillsMarketDir: filepath.Join(root, "runtime", "skills-market"),
		},
		ContainerHub: config.ContainerHubConfig{
			Enabled:             true,
			DefaultSandboxLevel: "run",
		},
	}
	return cfg
}

func testPromptContextDefinition(paths config.PathsConfig) catalog.AgentDefinition {
	return catalog.AgentDefinition{
		Key:      "demo-agent",
		AgentDir: filepath.Join(paths.AgentsDir, "demo-agent"),
		Runtime: map[string]any{
			"level": "run",
			"sandboxMounts": []map[string]any{
				{"platform": "skills-market", "mode": "ro"},
				{"platform": "agents"},
				{"platform": "teams"},
				{"platform": "automations"},
				{"platform": "chats"},
				{"platform": "models"},
				{"platform": "providers"},
				{"platform": "mcp-servers"},
				{"platform": "viewport-servers"},
				{"platform": "tools"},
				{"platform": "viewports"},
			},
		},
	}
}

func absTestPath(t *testing.T, path string) string {
	t.Helper()

	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		t.Fatalf("filepath.Abs(%q) error = %v", path, err)
	}
	return absolute
}

type testCatalogRegistry struct{}

func (testCatalogRegistry) Agents(string) []api.AgentSummary { return nil }
func (testCatalogRegistry) Teams() []api.TeamSummary         { return nil }
func (testCatalogRegistry) Skills(string) []api.SkillSummary { return nil }
func (testCatalogRegistry) SkillDefinition(string) (catalog.SkillDefinition, bool) {
	return catalog.SkillDefinition{}, false
}
func (testCatalogRegistry) Tools(string, string) []api.ToolSummary { return nil }
func (testCatalogRegistry) Tool(string) (api.ToolDetailResponse, bool) {
	return api.ToolDetailResponse{}, false
}
func (testCatalogRegistry) DefaultAgentKey() string { return "" }
func (testCatalogRegistry) AgentDefinition(string) (catalog.AgentDefinition, bool) {
	return catalog.AgentDefinition{}, false
}
func (testCatalogRegistry) TeamDefinition(string) (catalog.TeamDefinition, bool) {
	return catalog.TeamDefinition{}, false
}
func (testCatalogRegistry) Reload(context.Context, string) error { return nil }

var _ catalog.Registry = testCatalogRegistry{}
