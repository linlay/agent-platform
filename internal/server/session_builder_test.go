package server

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

func TestBuildSessionToolNamesDoesNotAutoAddInvokeAgents(t *testing.T) {
	got := buildSessionToolNames([]string{"datetime"}, true)
	want := []string{"datetime"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildSessionToolNames() = %#v, want %#v", got, want)
	}
}

func TestBuildSessionToolNamesKeepsExplicitInvokeAgents(t *testing.T) {
	got := buildSessionToolNames([]string{"datetime", contracts.InvokeAgentsToolName}, true)
	want := []string{"datetime", contracts.InvokeAgentsToolName}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildSessionToolNames() = %#v, want %#v", got, want)
	}
}

func TestBuildSessionToolNamesFiltersInvokeAgentsWhenDisallowed(t *testing.T) {
	got := buildSessionToolNames([]string{"datetime", contracts.InvokeAgentsToolName}, false)
	want := []string{"datetime"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildSessionToolNames() = %#v, want %#v", got, want)
	}
}

func TestBuildSessionToolNamesDoesNotAutoAddDesktopTools(t *testing.T) {
	got := buildSessionToolNames([]string{"datetime"}, true)
	want := []string{"datetime"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildSessionToolNames() = %#v, want %#v", got, want)
	}
}

func TestBuildSessionToolNamesKeepsExplicitDesktopTools(t *testing.T) {
	got := buildSessionToolNames([]string{"datetime", "desktop_action", "desktop_cdp"}, true)
	want := []string{"datetime", "desktop_action", "desktop_cdp"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildSessionToolNames() = %#v, want %#v", got, want)
	}
}

func TestBuildQuerySessionUsesCoderProfileDefaults(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, "agents")
	workspace := filepath.Join(root, "workspace")
	agentDir := filepath.Join(agentsDir, "coder-app")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("mkdir agent dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "agent.yml"), []byte(
		"key: coder-app\n"+
			"mode: CODER\n"+
			"modelConfig:\n"+
			"  modelKey: deepseek-v4-flash\n"+
			"runtimeConfig:\n"+
			"  workspaceRoot: "+filepath.ToSlash(workspace)+"\n",
	), 0o644); err != nil {
		t.Fatalf("write agent config: %v", err)
	}
	cfg := config.Config{
		Paths: config.PathsConfig{
			AgentsDir: agentsDir,
			ChatsDir:  filepath.Join(root, "chats"),
		},
		CoderPrompts: config.CoderPromptsConfig{
			SystemPrompt: "configured coder system prompt",
		},
	}
	registry, err := catalog.NewFileRegistry(cfg, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	def, ok := registry.AgentDefinition("coder-app")
	if !ok {
		t.Fatal("expected coder-app definition")
	}

	server := &Server{deps: Dependencies{Config: cfg, Registry: registry}}
	session, err := server.BuildQuerySession(context.Background(), api.QueryRequest{
		AgentKey: "coder-app",
		ChatID:   "chat-1",
		RunID:    "run-1",
		Role:     "user",
	}, chat.Summary{ChatID: "chat-1"}, def, querySessionBuildOptions{AllowInvokeAgents: true})
	if err != nil {
		t.Fatalf("build query session: %v", err)
	}

	wantTools := []string{"bash", "file_read", "file_write", "file_edit", "file_glob", "file_grep", "datetime", "regex", "vision_recognize"}
	if !reflect.DeepEqual(session.ToolNames, wantTools) {
		t.Fatalf("tool names = %#v, want %#v", session.ToolNames, wantTools)
	}
	if session.ResolvedBudget.Timeout != 1800 || session.ResolvedBudget.MaxSteps != 240 || session.ResolvedBudget.Model.MaxCalls != 240 || session.ResolvedBudget.Tool.MaxCalls != 200 {
		t.Fatalf("resolved budget = %#v, want CODER defaults", session.ResolvedBudget)
	}
	if session.Mode != catalog.AgentModeCoder {
		t.Fatalf("mode = %q, want %q", session.Mode, catalog.AgentModeCoder)
	}
	if session.AccessLevel != contracts.AccessLevelDefault {
		t.Fatalf("access level = %q, want default", session.AccessLevel)
	}
	if session.WorkspaceRoot != filepath.Clean(workspace) {
		t.Fatalf("workspace root = %q, want %q", session.WorkspaceRoot, filepath.Clean(workspace))
	}
	if session.CoderSystemPrompt != "configured coder system prompt" {
		t.Fatalf("coder system prompt = %q, want configured prompt", session.CoderSystemPrompt)
	}
	autoSession, err := server.BuildQuerySession(context.Background(), api.QueryRequest{
		AgentKey:    "coder-app",
		ChatID:      "chat-auto",
		RunID:       "run-auto",
		Role:        "user",
		AccessLevel: contracts.AccessLevelAutoApprove,
	}, chat.Summary{ChatID: "chat-auto"}, def, querySessionBuildOptions{AllowInvokeAgents: true})
	if err != nil {
		t.Fatalf("build auto access query session: %v", err)
	}
	if autoSession.AccessLevel != contracts.AccessLevelAutoApprove {
		t.Fatalf("access level = %q, want auto_approve", autoSession.AccessLevel)
	}
}

func TestBuildQuerySessionDefaultsHostWorkspaceToChatDir(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{
		Paths: config.PathsConfig{
			ChatsDir: filepath.Join(root, "chats"),
		},
	}
	def := catalog.AgentDefinition{
		Key:      "host-agent",
		Mode:     "REACT",
		ModelKey: "mock-model",
	}
	server := &Server{deps: Dependencies{Config: cfg}}
	session, err := server.BuildQuerySession(context.Background(), api.QueryRequest{
		AgentKey: "host-agent",
		ChatID:   "chat-1",
		RunID:    "run-1",
	}, chat.Summary{ChatID: "chat-1"}, def, querySessionBuildOptions{})
	if err != nil {
		t.Fatalf("build query session: %v", err)
	}
	want := filepath.Join(cfg.Paths.ChatsDir, "chat-1")
	if session.WorkspaceRoot != want {
		t.Fatalf("workspace root = %q, want %q", session.WorkspaceRoot, want)
	}
	if session.RuntimeContext.LocalPaths.ChatAttachmentsDir != want {
		t.Fatalf("chat attachments dir = %q, want %q", session.RuntimeContext.LocalPaths.ChatAttachmentsDir, want)
	}
	if stat, err := os.Stat(want); err != nil || !stat.IsDir() {
		t.Fatalf("expected chat dir to be created, stat=%#v err=%v", stat, err)
	}
}

func TestBuildQuerySessionDoesNotDefaultProxyWorkspaceToChatDir(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{
		Paths: config.PathsConfig{
			ChatsDir: filepath.Join(root, "chats"),
		},
	}
	def := catalog.AgentDefinition{
		Key:      "proxy-agent",
		Mode:     "PROXY",
		ModelKey: "mock-model",
	}
	server := &Server{deps: Dependencies{Config: cfg}}
	session, err := server.BuildQuerySession(context.Background(), api.QueryRequest{
		AgentKey: "proxy-agent",
		ChatID:   "chat-1",
		RunID:    "run-1",
	}, chat.Summary{ChatID: "chat-1"}, def, querySessionBuildOptions{})
	if err != nil {
		t.Fatalf("build query session: %v", err)
	}
	wantChatDir := filepath.Join(cfg.Paths.ChatsDir, "chat-1")
	if session.WorkspaceRoot != "" {
		t.Fatalf("workspace root = %q, want empty for proxy without workspaceRoot", session.WorkspaceRoot)
	}
	if session.RuntimeContext.LocalPaths.ChatAttachmentsDir != wantChatDir {
		t.Fatalf("chat attachments dir = %q, want %q", session.RuntimeContext.LocalPaths.ChatAttachmentsDir, wantChatDir)
	}
}

func TestBuildQuerySessionLoadsWorkspaceAgentsForCoder(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("workspace rules"), 0o644); err != nil {
		t.Fatalf("write workspace AGENTS.md: %v", err)
	}
	def := catalog.AgentDefinition{
		Key:      "coder-app",
		Mode:     catalog.AgentModeCoder,
		ModelKey: "mock-model",
		Workspace: catalog.AgentWorkspaceConfig{
			Root: workspace,
		},
	}
	cfg := config.Config{
		CoderSettings: config.CoderSettingsConfig{
			WorkspaceAgents: config.CoderWorkspaceAgentsConfig{Enabled: true, File: "AGENTS.md"},
		},
	}
	server := &Server{deps: Dependencies{Config: cfg}}
	session, err := server.BuildQuerySession(context.Background(), api.QueryRequest{
		AgentKey: "coder-app",
		ChatID:   "chat-1",
		RunID:    "run-1",
	}, chat.Summary{ChatID: "chat-1"}, def, querySessionBuildOptions{})
	if err != nil {
		t.Fatalf("build query session: %v", err)
	}
	if session.WorkspaceAgentsPrompt != "workspace rules" {
		t.Fatalf("workspace agents prompt = %q, want workspace rules", session.WorkspaceAgentsPrompt)
	}
}

func TestBuildQuerySessionLoadsConfiguredProjectPromptsForCoder(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	agentDir := filepath.Join(root, "agents", "coder-app")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("mkdir agent dir: %v", err)
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("workspace rules"), 0o644); err != nil {
		t.Fatalf("write workspace AGENTS.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "AGENTS.md"), []byte("agent rules"), 0o644); err != nil {
		t.Fatalf("write agent AGENTS.md: %v", err)
	}
	def := catalog.AgentDefinition{
		Key:      "coder-app",
		Mode:     catalog.AgentModeCoder,
		ModelKey: "mock-model",
		AgentDir: agentDir,
		Workspace: catalog.AgentWorkspaceConfig{
			Root: workspace,
		},
		Project: catalog.AgentProjectConfig{
			PromptFiles: []catalog.AgentProjectPromptFile{
				{Source: "workspace", Path: "AGENTS.md"},
				{Source: "workspace", Path: "CLAUDE.md"},
				{Source: "agent", Path: "AGENTS.md"},
			},
		},
	}
	cfg := config.Config{
		CoderSettings: config.CoderSettingsConfig{
			WorkspaceAgents: config.CoderWorkspaceAgentsConfig{Enabled: true, File: "IGNORED.md"},
		},
	}
	server := &Server{deps: Dependencies{Config: cfg}}
	session, err := server.BuildQuerySession(context.Background(), api.QueryRequest{
		AgentKey: "coder-app",
		ChatID:   "chat-1",
		RunID:    "run-1",
	}, chat.Summary{ChatID: "chat-1"}, def, querySessionBuildOptions{})
	if err != nil {
		t.Fatalf("build query session: %v", err)
	}
	want := "Workspace AGENTS.md\nworkspace rules\n\nAgent AGENTS.md\nagent rules"
	if session.WorkspaceAgentsPrompt != want {
		t.Fatalf("workspace agents prompt = %q, want %q", session.WorkspaceAgentsPrompt, want)
	}
}

func TestBuildQuerySessionErrorsWhenConfiguredProjectPromptUnreadable(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, "AGENTS.md"), 0o755); err != nil {
		t.Fatalf("mkdir workspace AGENTS.md directory: %v", err)
	}
	def := catalog.AgentDefinition{
		Key:      "coder-app",
		Mode:     catalog.AgentModeCoder,
		ModelKey: "mock-model",
		Workspace: catalog.AgentWorkspaceConfig{
			Root: workspace,
		},
		Project: catalog.AgentProjectConfig{
			PromptFiles: []catalog.AgentProjectPromptFile{{Source: "workspace", Path: "AGENTS.md"}},
		},
	}
	server := &Server{deps: Dependencies{}}
	_, err := server.BuildQuerySession(context.Background(), api.QueryRequest{
		AgentKey: "coder-app",
		ChatID:   "chat-1",
		RunID:    "run-1",
	}, chat.Summary{ChatID: "chat-1"}, def, querySessionBuildOptions{})
	if err == nil || !strings.Contains(err.Error(), "read workspace project prompt") {
		t.Fatalf("expected unreadable project prompt error, got %v", err)
	}
}

func TestBuildQuerySessionSkipsWorkspaceAgentsWhenDisabled(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("workspace rules"), 0o644); err != nil {
		t.Fatalf("write workspace AGENTS.md: %v", err)
	}
	def := catalog.AgentDefinition{
		Key:       "coder-app",
		Mode:      catalog.AgentModeCoder,
		ModelKey:  "mock-model",
		Workspace: catalog.AgentWorkspaceConfig{Root: workspace},
	}
	cfg := config.Config{
		CoderSettings: config.CoderSettingsConfig{
			WorkspaceAgents: config.CoderWorkspaceAgentsConfig{Enabled: false, File: "AGENTS.md"},
		},
	}
	server := &Server{deps: Dependencies{Config: cfg}}
	session, err := server.BuildQuerySession(context.Background(), api.QueryRequest{
		AgentKey: "coder-app",
		ChatID:   "chat-1",
		RunID:    "run-1",
	}, chat.Summary{ChatID: "chat-1"}, def, querySessionBuildOptions{})
	if err != nil {
		t.Fatalf("build query session: %v", err)
	}
	if session.WorkspaceAgentsPrompt != "" {
		t.Fatalf("expected workspace agents prompt to be skipped, got %q", session.WorkspaceAgentsPrompt)
	}
}

func TestBuildQuerySessionDoesNotLoadWorkspaceAgentsForNonCoder(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("workspace rules"), 0o644); err != nil {
		t.Fatalf("write workspace AGENTS.md: %v", err)
	}
	def := catalog.AgentDefinition{
		Key:       "react-app",
		Mode:      "REACT",
		ModelKey:  "mock-model",
		Workspace: catalog.AgentWorkspaceConfig{Root: workspace},
	}
	cfg := config.Config{
		CoderSettings: config.CoderSettingsConfig{
			WorkspaceAgents: config.CoderWorkspaceAgentsConfig{Enabled: true, File: "AGENTS.md"},
		},
	}
	server := &Server{deps: Dependencies{Config: cfg}}
	session, err := server.BuildQuerySession(context.Background(), api.QueryRequest{
		AgentKey: "react-app",
		ChatID:   "chat-1",
		RunID:    "run-1",
	}, chat.Summary{ChatID: "chat-1"}, def, querySessionBuildOptions{})
	if err != nil {
		t.Fatalf("build query session: %v", err)
	}
	if session.WorkspaceAgentsPrompt != "" {
		t.Fatalf("expected non-CODER workspace agents prompt to be skipped, got %q", session.WorkspaceAgentsPrompt)
	}
}

func TestBuildQuerySessionErrorsWhenWorkspaceAgentsUnreadable(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, "AGENTS.md"), 0o755); err != nil {
		t.Fatalf("mkdir workspace AGENTS.md directory: %v", err)
	}
	def := catalog.AgentDefinition{
		Key:       "coder-app",
		Mode:      catalog.AgentModeCoder,
		ModelKey:  "mock-model",
		Workspace: catalog.AgentWorkspaceConfig{Root: workspace},
	}
	cfg := config.Config{
		CoderSettings: config.CoderSettingsConfig{
			WorkspaceAgents: config.CoderWorkspaceAgentsConfig{Enabled: true, File: "AGENTS.md"},
		},
	}
	server := &Server{deps: Dependencies{Config: cfg}}
	_, err := server.BuildQuerySession(context.Background(), api.QueryRequest{
		AgentKey: "coder-app",
		ChatID:   "chat-1",
		RunID:    "run-1",
	}, chat.Summary{ChatID: "chat-1"}, def, querySessionBuildOptions{})
	if err == nil || !strings.Contains(err.Error(), "read workspace AGENTS prompt") {
		t.Fatalf("expected workspace AGENTS read error, got %v", err)
	}
}

func TestBuildQuerySessionValidatesExpectedGitBranch(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := writeGitHead(workspace, "main"); err != nil {
		t.Fatalf("write git head: %v", err)
	}
	def := catalog.AgentDefinition{
		Key:      "coder-app",
		Mode:     catalog.AgentModeCoder,
		ModelKey: "mock-model",
		Workspace: catalog.AgentWorkspaceConfig{
			Root: workspace,
		},
		Project: catalog.AgentProjectConfig{
			Git: catalog.AgentProjectGitConfig{ExpectedBranch: "main"},
		},
	}
	server := &Server{deps: Dependencies{}}
	if _, err := server.BuildQuerySession(context.Background(), api.QueryRequest{
		AgentKey: "coder-app",
		ChatID:   "chat-1",
		RunID:    "run-1",
	}, chat.Summary{ChatID: "chat-1"}, def, querySessionBuildOptions{}); err != nil {
		t.Fatalf("build query session: %v", err)
	}
}

func TestBuildQuerySessionErrorsWhenGitBranchMismatches(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := writeGitHead(workspace, "feature"); err != nil {
		t.Fatalf("write git head: %v", err)
	}
	def := catalog.AgentDefinition{
		Key:      "coder-app",
		Mode:     catalog.AgentModeCoder,
		ModelKey: "mock-model",
		Workspace: catalog.AgentWorkspaceConfig{
			Root: workspace,
		},
		Project: catalog.AgentProjectConfig{
			Git: catalog.AgentProjectGitConfig{ExpectedBranch: "main"},
		},
	}
	server := &Server{deps: Dependencies{}}
	_, err := server.BuildQuerySession(context.Background(), api.QueryRequest{
		AgentKey: "coder-app",
		ChatID:   "chat-1",
		RunID:    "run-1",
	}, chat.Summary{ChatID: "chat-1"}, def, querySessionBuildOptions{})
	if err == nil || !strings.Contains(err.Error(), "workspace git branch mismatch") {
		t.Fatalf("expected git branch mismatch error, got %v", err)
	}
}

func TestBuildQuerySessionErrorsWhenExpectedGitBranchNeedsRepo(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	def := catalog.AgentDefinition{
		Key:      "coder-app",
		Mode:     catalog.AgentModeCoder,
		ModelKey: "mock-model",
		Workspace: catalog.AgentWorkspaceConfig{
			Root: workspace,
		},
		Project: catalog.AgentProjectConfig{
			Git: catalog.AgentProjectGitConfig{ExpectedBranch: "main"},
		},
	}
	server := &Server{deps: Dependencies{}}
	_, err := server.BuildQuerySession(context.Background(), api.QueryRequest{
		AgentKey: "coder-app",
		ChatID:   "chat-1",
		RunID:    "run-1",
	}, chat.Summary{ChatID: "chat-1"}, def, querySessionBuildOptions{})
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("expected git repository error, got %v", err)
	}
}

func writeGitHead(workspace string, branch string) error {
	gitDir := filepath.Join(workspace, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/"+branch+"\n"), 0o644)
}

func TestBuildQuerySessionPlanningModeOnlyAppliesToCoder(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, "agents")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(filepath.Join(agentsDir, "coder-app"), 0o755); err != nil {
		t.Fatalf("mkdir coder dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "coder-app", "agent.yml"), []byte(
		"key: coder-app\n"+
			"mode: CODER\n"+
			"modelConfig:\n"+
			"  modelKey: mock-model\n"+
			"runtimeConfig:\n"+
			"  workspaceRoot: "+filepath.ToSlash(workspace)+"\n",
	), 0o644); err != nil {
		t.Fatalf("write coder config: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(agentsDir, "react-app"), 0o755); err != nil {
		t.Fatalf("mkdir react dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "react-app", "agent.yml"), []byte(
		"key: react-app\n"+
			"mode: REACT\n"+
			"modelConfig:\n"+
			"  modelKey: mock-model\n",
	), 0o644); err != nil {
		t.Fatalf("write react config: %v", err)
	}
	cfg := config.Config{
		Paths: config.PathsConfig{
			AgentsDir: agentsDir,
			ChatsDir:  filepath.Join(root, "chats"),
		},
	}
	registry, err := catalog.NewFileRegistry(cfg, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	server := &Server{deps: Dependencies{Config: cfg, Registry: registry}}
	enabled := true

	coderDef, ok := registry.AgentDefinition("coder-app")
	if !ok {
		t.Fatal("expected coder definition")
	}
	coderSession, err := server.BuildQuerySession(context.Background(), api.QueryRequest{
		AgentKey:     "coder-app",
		ChatID:       "chat-coder",
		RunID:        "run-coder",
		PlanningMode: &enabled,
	}, chat.Summary{ChatID: "chat-coder"}, coderDef, querySessionBuildOptions{})
	if err != nil {
		t.Fatalf("build coder session: %v", err)
	}
	if !coderSession.PlanningMode {
		t.Fatalf("expected CODER planning mode to be enabled")
	}

	reactDef, ok := registry.AgentDefinition("react-app")
	if !ok {
		t.Fatal("expected react definition")
	}
	reactSession, err := server.BuildQuerySession(context.Background(), api.QueryRequest{
		AgentKey:     "react-app",
		ChatID:       "chat-react",
		RunID:        "run-react",
		PlanningMode: &enabled,
	}, chat.Summary{ChatID: "chat-react"}, reactDef, querySessionBuildOptions{})
	if err != nil {
		t.Fatalf("build react session: %v", err)
	}
	if reactSession.PlanningMode {
		t.Fatalf("did not expect non-CODER planning mode")
	}

	disabledSession, err := server.BuildQuerySession(context.Background(), api.QueryRequest{
		AgentKey: "coder-app",
		ChatID:   "chat-disabled",
		RunID:    "run-disabled",
	}, chat.Summary{ChatID: "chat-disabled"}, coderDef, querySessionBuildOptions{})
	if err != nil {
		t.Fatalf("build disabled session: %v", err)
	}
	if disabledSession.PlanningMode {
		t.Fatalf("did not expect CODER planning mode without top-level planningMode")
	}

	disabledFlag := false
	falseSession, err := server.BuildQuerySession(context.Background(), api.QueryRequest{
		AgentKey:     "coder-app",
		ChatID:       "chat-false",
		RunID:        "run-false",
		PlanningMode: &disabledFlag,
	}, chat.Summary{ChatID: "chat-false"}, coderDef, querySessionBuildOptions{})
	if err != nil {
		t.Fatalf("build false session: %v", err)
	}
	if falseSession.PlanningMode {
		t.Fatalf("did not expect CODER planning mode with planningMode=false")
	}

	paramsSession, err := server.BuildQuerySession(context.Background(), api.QueryRequest{
		AgentKey: "coder-app",
		ChatID:   "chat-params",
		RunID:    "run-params",
		Params:   map[string]any{"planningMode": "true"},
	}, chat.Summary{ChatID: "chat-params"}, coderDef, querySessionBuildOptions{})
	if err != nil {
		t.Fatalf("build params session: %v", err)
	}
	if paramsSession.PlanningMode {
		t.Fatalf("did not expect params.planningMode to enable planning mode")
	}
}
