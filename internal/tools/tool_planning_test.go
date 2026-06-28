package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	planutil "agent-platform/internal/planning"
)

func TestFinalizePlanningCreatesMarkdownFile(t *testing.T) {
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

	result, err := executor.Invoke(context.Background(), FinalizePlanningToolName, map[string]any{
		"markdown": standardPlanningMarkdown("改造 CODER planningMode"),
	}, execCtx)
	if err != nil {
		t.Fatalf("invoke finalize_planning: %v", err)
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
	if execCtx.PlanningState.ToolName != FinalizePlanningToolName {
		t.Fatalf("expected planning state tool name %q, got %#v", FinalizePlanningToolName, execCtx.PlanningState)
	}
}

func TestFinalizePlanningDoesNotPersistSnapshotRefs(t *testing.T) {
	root := t.TempDir()
	store, err := chat.NewFileStore(root)
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat_1", "coder", "", "plan it"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	executor := &RuntimeToolExecutor{
		cfg:   config.Config{Paths: config.PathsConfig{ChatsDir: root}},
		chats: store,
	}
	execCtx := &ExecutionContext{
		Session: QuerySession{
			RunID:        "run_refs",
			ChatID:       "chat_1",
			AgentKey:     "coder",
			PlanningMode: true,
		},
	}

	firstMarkdown := "# Persisted Plan V1\n\n## Summary\nFirst plan"
	if result, err := executor.Invoke(context.Background(), FinalizePlanningToolName, map[string]any{"markdown": firstMarkdown}, execCtx); err != nil || result.ExitCode != 0 {
		t.Fatalf("first finalize_planning result=%#v err=%v", result, err)
	}
	execCtx.PlanningState = nil
	execCtx.PlanningRevision = 2
	secondMarkdown := "# Persisted Plan V2\n\n## Summary\nSecond plan"
	if result, err := executor.Invoke(context.Background(), FinalizePlanningToolName, map[string]any{"markdown": secondMarkdown}, execCtx); err != nil || result.ExitCode != 0 {
		t.Fatalf("second finalize_planning result=%#v err=%v", result, err)
	}

	jsonlBytes, readErr := os.ReadFile(filepath.Join(root, "chat_1.jsonl"))
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatalf("read chat jsonl: %v", readErr)
	}
	if len(jsonlBytes) > 0 {
		t.Fatalf("finalize_planning should not persist planning refs in jsonl, got:\n%s", string(jsonlBytes))
	}
}

func TestFinalizePlanningPreservesMarkdownExactly(t *testing.T) {
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
	result, err := executor.Invoke(context.Background(), FinalizePlanningToolName, map[string]any{
		"markdown": markdown,
	}, execCtx)
	if err != nil {
		t.Fatalf("invoke finalize_planning: %v", err)
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

func TestFinalizePlanningRejectsSecondWrite(t *testing.T) {
	executor := &RuntimeToolExecutor{cfg: config.Config{Paths: config.PathsConfig{ChatsDir: t.TempDir()}}}
	execCtx := &ExecutionContext{
		Request: api.QueryRequest{Message: "plan"},
		Session: QuerySession{
			RunID:        "run_123",
			PlanningMode: true,
		},
		PlanningState: &PlanningRuntimeState{Markdown: "# Existing\n"},
	}
	result, err := executor.Invoke(context.Background(), FinalizePlanningToolName, map[string]any{
		"markdown": standardPlanningMarkdown("Plan"),
	}, execCtx)
	if err != nil {
		t.Fatalf("invoke finalize_planning: %v", err)
	}
	if result.Error != "finalize_planning_already_exists" || result.ExitCode == 0 {
		t.Fatalf("expected already exists error, got %#v", result)
	}
}

func TestFinalizePlanningRejectsEmptyMarkdown(t *testing.T) {
	executor := &RuntimeToolExecutor{cfg: config.Config{Paths: config.PathsConfig{ChatsDir: t.TempDir()}}}
	execCtx := &ExecutionContext{
		Request: api.QueryRequest{Message: "plan"},
		Session: QuerySession{
			RunID:        "run_123",
			PlanningMode: true,
		},
	}
	result, err := executor.Invoke(context.Background(), FinalizePlanningToolName, map[string]any{
		"markdown": "",
	}, execCtx)
	if err != nil {
		t.Fatalf("invoke finalize_planning: %v", err)
	}
	if result.Error != "missing_markdown" || result.ExitCode == 0 {
		t.Fatalf("expected missing markdown error, got %#v", result)
	}
}

func TestPlanGetTasksReturnsEmptySnapshotBeforeTasksExist(t *testing.T) {
	executor := &RuntimeToolExecutor{}
	execCtx := &ExecutionContext{
		Session: QuerySession{
			RunID:  "run_tasks",
			ChatID: "chat_1",
		},
	}

	result, err := executor.Invoke(context.Background(), PlanGetTasksToolName, map[string]any{}, execCtx)
	if err != nil {
		t.Fatalf("invoke plan_get_tasks: %v", err)
	}
	if result.ExitCode != 0 || result.Error != "" {
		t.Fatalf("expected empty plan snapshot success, got %#v", result)
	}
	if got := AnyStringNode(result.Structured["planId"]); got != "run_tasks_plan" {
		t.Fatalf("planId=%q want run_tasks_plan", got)
	}
	plan, _ := result.Structured["plan"].([]map[string]any)
	if len(plan) != 0 {
		t.Fatalf("expected empty plan, got %#v", result.Structured["plan"])
	}
	if execCtx.PlanState == nil {
		t.Fatalf("expected plan state to be initialized")
	}
}

func TestPlanUpdateTaskSupportsInProgressAndDescriptionUpdate(t *testing.T) {
	executor := &RuntimeToolExecutor{}
	execCtx := &ExecutionContext{
		Session: QuerySession{RunID: "run_tasks"},
		PlanState: &PlanRuntimeState{
			PlanID: "run_tasks_plan",
			Tasks: []PlanTask{{
				TaskID:      "task_1",
				Status:      "init",
				Description: "old description",
			}},
		},
	}

	result, err := executor.Invoke(context.Background(), PlanUpdateTaskToolName, map[string]any{
		"taskId":      "task_1",
		"status":      "in_progress",
		"description": "new description",
	}, execCtx)
	if err != nil {
		t.Fatalf("invoke plan_update_task in_progress: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected in_progress update success, got %#v", result)
	}
	if execCtx.PlanState.ActiveTaskID != "task_1" {
		t.Fatalf("active task=%q want task_1", execCtx.PlanState.ActiveTaskID)
	}
	if task := execCtx.PlanState.Tasks[0]; task.Status != "in_progress" || task.Description != "new description" {
		t.Fatalf("unexpected task after in_progress update: %#v", task)
	}
	if got := AnyStringNode(result.Structured["currentTaskId"]); got != "task_1" {
		t.Fatalf("currentTaskId=%q want task_1", got)
	}

	result, err = executor.Invoke(context.Background(), PlanUpdateTaskToolName, map[string]any{
		"taskId": "task_1",
		"status": "completed",
	}, execCtx)
	if err != nil {
		t.Fatalf("invoke plan_update_task completed: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected completed update success, got %#v", result)
	}
	if execCtx.PlanState.ActiveTaskID != "" {
		t.Fatalf("active task=%q want empty", execCtx.PlanState.ActiveTaskID)
	}
	if _, ok := result.Structured["currentTaskId"]; ok {
		t.Fatalf("did not expect currentTaskId after terminal update, got %#v", result.Structured)
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
- Use finalize_planning markdown field

## Test Plan
- Run go test

## Assumptions
- Use chat .tools/plans
`
}
