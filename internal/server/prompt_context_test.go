package server

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/config"
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
	if paths.ViewportsDir != absTestPath(t, filepath.Join(cfg.Paths.RegistriesDir, "viewports")) {
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

	paths := resolveLocalPaths(cfg.Paths, "chat-1", agentDir)
	if paths.AgentDir != agentDir {
		t.Fatalf("agent dir = %q", paths.AgentDir)
	}
	if paths.SkillsDir != filepath.Join(agentDir, "skills") {
		t.Fatalf("skills dir = %q", paths.SkillsDir)
	}
	if paths.SkillsMarketDir != cfg.Paths.SkillsMarketDir {
		t.Fatalf("skills market dir = %q", paths.SkillsMarketDir)
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
	if paths.ViewportsDir != filepath.Join(cfg.Paths.RegistriesDir, "viewports") {
		t.Fatalf("viewports dir = %q", paths.ViewportsDir)
	}
	if paths.ChatAttachmentsDir != filepath.Join(cfg.Paths.ChatsDir, "chat-1") {
		t.Fatalf("chat attachments dir = %q", paths.ChatAttachmentsDir)
	}
	if paths.WorkingDirectory == "" {
		t.Fatal("expected working directory to be populated")
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
			Sandbox: map[string]any{
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
			Sandbox: map[string]any{
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
		t.Fatalf("expected legacy sandbox tag to have no effect, got %#v", context.SandboxContext)
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
}

func testPromptContextConfig(t *testing.T) config.Config {
	t.Helper()

	root := t.TempDir()
	cfg := config.Config{
		Paths: config.PathsConfig{
			RegistriesDir:   filepath.Join(root, "runtime", "registries"),
			ToolsDir:        filepath.Join(root, "runtime", "registries", "tools"),
			OwnerDir:        filepath.Join(root, "runtime", "owner"),
			AgentsDir:       filepath.Join(root, "runtime", "agents"),
			TeamsDir:        filepath.Join(root, "runtime", "teams"),
			RootDir:         filepath.Join(root, "runtime", "root"),
			SchedulesDir:    filepath.Join(root, "runtime", "schedules"),
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
		Sandbox: map[string]any{
			"level": "run",
			"extraMounts": []map[string]any{
				{"platform": "skills-market"},
				{"platform": "agents"},
				{"platform": "teams"},
				{"platform": "schedules"},
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
