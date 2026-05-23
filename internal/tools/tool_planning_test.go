package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	planutil "agent-platform/internal/planning"
)

func TestPlanningWriteCreatesMarkdownFile(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{cfg: config.Config{Paths: config.PathsConfig{ChatsDir: root}}}
	execCtx := &ExecutionContext{
		Request: api.QueryRequest{Message: "改造 CODER planningMode"},
		Session: QuerySession{
			RequestID:    "req_1",
			RunID:        "run_123",
			ChatID:       "chat_1",
			AgentKey:     "coder",
			PlanningMode: true,
		},
	}
	stalePlanningFile := filepath.Join(root, "plans", "run_123_planning.md")
	if err := os.MkdirAll(filepath.Dir(stalePlanningFile), 0o755); err != nil {
		t.Fatalf("mkdir stale planning dir: %v", err)
	}
	if err := os.WriteFile(stalePlanningFile, []byte("stale draft"), 0o644); err != nil {
		t.Fatalf("write stale planning file: %v", err)
	}

	result, err := executor.Invoke(context.Background(), "planning_write", map[string]any{
		"title":                  "改造 CODER planningMode",
		"summary":                "Write a standard planning document.",
		"publicEventsAndStorage": []any{"Keep planning lifecycle events unchanged"},
		"implementationChanges":  []any{"Write the markdown file"},
		"interfaces":             []any{"Use planning_write structured fields"},
		"testPlan":               []any{"Run go test"},
		"assumptions":            []any{"Use CHATS_DIR/plans"},
	}, execCtx)
	if err != nil {
		t.Fatalf("invoke planning_write: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected success, got %#v", result)
	}
	planningID := AnyStringNode(result.Structured["planningId"])
	if planningID != "run_123_planning" {
		t.Fatalf("unexpected planningId %q", planningID)
	}
	planningFile := AnyStringNode(result.Structured["planningFile"])
	if planningFile != filepath.Join(root, "plans", planningID+".md") {
		t.Fatalf("unexpected planningFile %q", planningFile)
	}
	data, readErr := os.ReadFile(planningFile)
	if readErr != nil {
		t.Fatalf("read planning file: %v", readErr)
	}
	text := string(data)
	expected := planutil.RenderMarkdown(planutil.Spec{
		Title:                  "改造 CODER planningMode",
		Summary:                "Write a standard planning document.",
		PublicEventsAndStorage: []string{"Keep planning lifecycle events unchanged"},
		ImplementationChanges:  []string{"Write the markdown file"},
		Interfaces:             []string{"Use planning_write structured fields"},
		TestPlan:               []string{"Run go test"},
		Assumptions:            []string{"Use CHATS_DIR/plans"},
	})
	if text != expected {
		t.Fatalf("planning file mismatch\nwant:\n%s\ngot:\n%s", expected, text)
	}
	for _, want := range []string{"# 改造 CODER planningMode", "## Summary", "## Public Events And Storage", "## Implementation Changes", "## Interfaces", "## Test Plan", "## Assumptions"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected markdown to contain %q, got:\n%s", want, text)
		}
	}
	if execCtx.PlanningState == nil || execCtx.PlanningState.PlanningID != planningID {
		t.Fatalf("expected execution context planning state, got %#v", execCtx.PlanningState)
	}
}

func TestPlanningWriteRejectsSecondWrite(t *testing.T) {
	executor := &RuntimeToolExecutor{cfg: config.Config{Paths: config.PathsConfig{ChatsDir: t.TempDir()}}}
	execCtx := &ExecutionContext{
		Request: api.QueryRequest{Message: "plan"},
		Session: QuerySession{
			RunID:        "run_123",
			PlanningMode: true,
		},
		PlanningState: &PlanningRuntimeState{Markdown: "# Existing\n"},
	}
	result, err := executor.Invoke(context.Background(), "planning_write", map[string]any{
		"title":                  "Plan",
		"summary":                "Summary",
		"publicEventsAndStorage": []any{"Event"},
		"implementationChanges":  []any{"Change"},
		"interfaces":             []any{"Interface"},
		"testPlan":               []any{"Test"},
		"assumptions":            []any{"Assumption"},
	}, execCtx)
	if err != nil {
		t.Fatalf("invoke planning_write: %v", err)
	}
	if result.Error != "planning_write_already_exists" || result.ExitCode == 0 {
		t.Fatalf("expected already exists error, got %#v", result)
	}
}

func TestPlanningWriteRejectsNestedMarkdownPlan(t *testing.T) {
	executor := &RuntimeToolExecutor{cfg: config.Config{Paths: config.PathsConfig{ChatsDir: t.TempDir()}}}
	execCtx := &ExecutionContext{
		Request: api.QueryRequest{Message: "plan"},
		Session: QuerySession{
			RunID:        "run_123",
			PlanningMode: true,
		},
	}
	result, err := executor.Invoke(context.Background(), "planning_write", map[string]any{
		"title":                  "Plan",
		"summary":                "First plan\n\n## Public Events And Storage\nNested section",
		"publicEventsAndStorage": []any{"Event"},
		"implementationChanges":  []any{"Change"},
		"interfaces":             []any{"Interface"},
		"testPlan":               []any{"Test"},
		"assumptions":            []any{"Assumption"},
	}, execCtx)
	if err != nil {
		t.Fatalf("invoke planning_write: %v", err)
	}
	if result.Error != "invalid_planning_content" || result.ExitCode == 0 {
		t.Fatalf("expected invalid planning content error, got %#v", result)
	}
}
