package server

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
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
			"type: CODER\n"+
			"mode: REACT\n"+
			"modelConfig:\n"+
			"  modelKey: deepseek-v4-flash\n"+
			"workspaceConfig:\n"+
			"  root: "+filepath.ToSlash(workspace)+"\n",
	), 0o644); err != nil {
		t.Fatalf("write agent config: %v", err)
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

	wantTools := []string{"bash", "file_read", "file_write", "file_grep", "datetime", "ask_user_question", "desktop_cdp"}
	if !reflect.DeepEqual(session.ToolNames, wantTools) {
		t.Fatalf("tool names = %#v, want %#v", session.ToolNames, wantTools)
	}
	if session.ResolvedBudget.RunTimeoutMs != 3600000 || session.ResolvedBudget.Model.MaxCalls != 240 || session.ResolvedBudget.Tool.MaxCalls != 300 {
		t.Fatalf("resolved budget = %#v, want CODER defaults", session.ResolvedBudget)
	}
	if session.ReactMaxSteps != 160 {
		t.Fatalf("react max steps = %d, want 160", session.ReactMaxSteps)
	}
	if session.WorkspaceRoot != filepath.Clean(workspace) {
		t.Fatalf("workspace root = %q, want %q", session.WorkspaceRoot, filepath.Clean(workspace))
	}
}
