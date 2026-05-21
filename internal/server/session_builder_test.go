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

	wantTools := []string{"bash", "file_read", "file_write", "file_edit", "file_grep", "datetime"}
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

func TestBuildQuerySessionPlanningModeOnlyAppliesToCoder(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, "agents")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(filepath.Join(agentsDir, "coder-app"), 0o755); err != nil {
		t.Fatalf("mkdir coder dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "coder-app", "agent.yml"), []byte(
		"key: coder-app\n"+
			"type: CODER\n"+
			"mode: REACT\n"+
			"modelConfig:\n"+
			"  modelKey: mock-model\n"+
			"workspaceConfig:\n"+
			"  root: "+filepath.ToSlash(workspace)+"\n",
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
