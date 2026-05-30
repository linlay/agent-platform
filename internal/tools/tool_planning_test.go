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
	stalePlanningFile := filepath.Join(root, "plans", "run_123_planning_1.md")
	if err := os.MkdirAll(filepath.Dir(stalePlanningFile), 0o755); err != nil {
		t.Fatalf("mkdir stale planning dir: %v", err)
	}
	if err := os.WriteFile(stalePlanningFile, []byte("stale draft"), 0o644); err != nil {
		t.Fatalf("write stale planning file: %v", err)
	}

	result, err := executor.Invoke(context.Background(), "planning_write", map[string]any{
		"markdown": standardPlanningMarkdown("改造 CODER planningMode"),
	}, execCtx)
	if err != nil {
		t.Fatalf("invoke planning_write: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected success, got %#v", result)
	}
	planningID := AnyStringNode(result.Structured["planningId"])
	if planningID != "run_123_planning_1" {
		t.Fatalf("unexpected planningId %q", planningID)
	}
	planningFile := AnyStringNode(result.Structured["planningFile"])
	if planningFile != planutil.PlanningFileForChat(root, "chat_1", planningID) {
		t.Fatalf("unexpected planningFile %q", planningFile)
	}
	for _, key := range []string{"title", "status", "updatedAt"} {
		if _, ok := result.Structured[key]; ok {
			t.Fatalf("did not expect structured %s in markdown-only result: %#v", key, result.Structured)
		}
	}
	data, readErr := os.ReadFile(planningFile)
	if readErr != nil {
		t.Fatalf("read planning file: %v", readErr)
	}
	text := string(data)
	expected := standardPlanningMarkdown("改造 CODER planningMode")
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

func TestPlanningWritePreservesMarkdownExactly(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{cfg: config.Config{Paths: config.PathsConfig{ChatsDir: root}}}
	execCtx := &ExecutionContext{
		Session: QuerySession{
			RunID:        "run_raw",
			ChatID:       "chat_1",
			PlanningMode: true,
		},
	}
	markdown := "## Summary\nNo backend heading normalization."
	result, err := executor.Invoke(context.Background(), "planning_write", map[string]any{
		"markdown": markdown,
	}, execCtx)
	if err != nil {
		t.Fatalf("invoke planning_write: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected success, got %#v", result)
	}
	data, readErr := os.ReadFile(AnyStringNode(result.Structured["planningFile"]))
	if readErr != nil {
		t.Fatalf("read planning file: %v", readErr)
	}
	if string(data) != markdown {
		t.Fatalf("planning file should preserve markdown exactly\nwant:%q\ngot:%q", markdown, string(data))
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
		"markdown": standardPlanningMarkdown("Plan"),
	}, execCtx)
	if err != nil {
		t.Fatalf("invoke planning_write: %v", err)
	}
	if result.Error != "planning_write_already_exists" || result.ExitCode == 0 {
		t.Fatalf("expected already exists error, got %#v", result)
	}
}

func TestPlanningWriteRejectsEmptyMarkdown(t *testing.T) {
	executor := &RuntimeToolExecutor{cfg: config.Config{Paths: config.PathsConfig{ChatsDir: t.TempDir()}}}
	execCtx := &ExecutionContext{
		Request: api.QueryRequest{Message: "plan"},
		Session: QuerySession{
			RunID:        "run_123",
			PlanningMode: true,
		},
	}
	result, err := executor.Invoke(context.Background(), "planning_write", map[string]any{
		"markdown": "",
	}, execCtx)
	if err != nil {
		t.Fatalf("invoke planning_write: %v", err)
	}
	if result.Error != "missing_markdown" || result.ExitCode == 0 {
		t.Fatalf("expected missing markdown error, got %#v", result)
	}
}

func standardPlanningMarkdown(title string) string {
	return `# ` + title + `

## Summary
Write a standard planning document.

## Public Events And Storage
- Keep planning lifecycle events unchanged

## Implementation Changes
- Write the markdown file

## Interfaces
- Use planning_write markdown field

## Test Plan
- Run go test

## Assumptions
- Use chat .tools/plans
`
}
