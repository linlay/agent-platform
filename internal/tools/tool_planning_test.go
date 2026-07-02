package tools

import (
	"context"
	"encoding/json"
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

func TestPlanGetTasksRestoresLatestSnapshot(t *testing.T) {
	root := t.TempDir()
	writePlanTasksSnapshotForTest(t, root, "chat_1", "run_old", `{"version":1,"chatId":"chat_1","runId":"run_old","planId":"old_plan","currentTaskId":"task_2","updatedAt":200,"tasks":[{"taskId":"task_1","description":"done","status":"completed"},{"taskId":"task_2","description":"next","status":"in_progress"}]}`)
	executor := &RuntimeToolExecutor{cfg: config.Config{Paths: config.PathsConfig{ChatsDir: root}}}
	execCtx := &ExecutionContext{
		Session: QuerySession{
			RunID:  "run_new",
			ChatID: "chat_1",
		},
	}

	result, err := executor.Invoke(context.Background(), PlanGetTasksToolName, map[string]any{}, execCtx)
	if err != nil {
		t.Fatalf("invoke plan_get_tasks: %v", err)
	}
	if result.ExitCode != 0 || result.Error != "" {
		t.Fatalf("expected restored plan snapshot success, got %#v", result)
	}
	if got := AnyStringNode(result.Structured["planId"]); got != "old_plan" {
		t.Fatalf("planId=%q want old_plan", got)
	}
	if got := AnyStringNode(result.Structured["currentTaskId"]); got != "task_2" {
		t.Fatalf("currentTaskId=%q want task_2", got)
	}
	plan, _ := result.Structured["plan"].([]map[string]any)
	if len(plan) != 2 || AnyStringNode(plan[1]["taskId"]) != "task_2" {
		t.Fatalf("unexpected restored plan: %#v", result.Structured["plan"])
	}
	if execCtx.PlanState == nil || execCtx.PlanState.PlanID != "old_plan" || execCtx.PlanState.ActiveTaskID != "task_2" {
		t.Fatalf("expected execCtx plan state to be restored, got %#v", execCtx.PlanState)
	}
}

func TestPlanAddTasksPersistsPlanTaskSnapshot(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{cfg: config.Config{Paths: config.PathsConfig{ChatsDir: root}}}
	execCtx := &ExecutionContext{
		Session: QuerySession{
			RunID:  "run_tasks",
			ChatID: "chat_1",
		},
	}

	result, err := executor.Invoke(context.Background(), PlanAddTasksToolName, map[string]any{
		"tasks": []any{
			map[string]any{"taskId": "task_1", "description": "first task"},
			map[string]any{"taskId": "task_2", "description": "second task", "status": "in_progress"},
		},
	}, execCtx)
	if err != nil {
		t.Fatalf("invoke plan_add_tasks: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected plan_add_tasks success, got %#v", result)
	}

	snapshot := readPlanTasksSnapshotForTest(t, root, "chat_1", "run_tasks")
	if snapshot.Version != 1 || snapshot.ChatID != "chat_1" || snapshot.RunID != "run_tasks" || snapshot.PlanID != "run_tasks_plan" {
		t.Fatalf("unexpected snapshot metadata: %#v", snapshot)
	}
	if snapshot.UpdatedAt <= 0 {
		t.Fatalf("expected updatedAt to be populated, got %#v", snapshot)
	}
	if len(snapshot.Tasks) != 2 || snapshot.Tasks[0].TaskID != "task_1" || snapshot.Tasks[0].Status != "init" ||
		snapshot.Tasks[1].TaskID != "task_2" || snapshot.Tasks[1].Status != "in_progress" {
		t.Fatalf("unexpected snapshot tasks: %#v", snapshot.Tasks)
	}
}

func TestPlanUpdateTaskRestoresSnapshotAndWritesNewRunSnapshot(t *testing.T) {
	root := t.TempDir()
	writePlanTasksSnapshotForTest(t, root, "chat_1", "run_old", `{"version":1,"chatId":"chat_1","runId":"run_old","planId":"old_plan","updatedAt":200,"tasks":[{"taskId":"task_1","description":"first","status":"init"}]}`)
	executor := &RuntimeToolExecutor{cfg: config.Config{Paths: config.PathsConfig{ChatsDir: root}}}
	execCtx := &ExecutionContext{
		Session: QuerySession{
			RunID:  "run_new",
			ChatID: "chat_1",
		},
	}

	result, err := executor.Invoke(context.Background(), PlanUpdateTaskToolName, map[string]any{
		"taskId": "task_1",
		"status": "completed",
	}, execCtx)
	if err != nil {
		t.Fatalf("invoke plan_update_task: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected plan_update_task success, got %#v", result)
	}

	snapshot := readPlanTasksSnapshotForTest(t, root, "chat_1", "run_new")
	if snapshot.RunID != "run_new" || snapshot.PlanID != "old_plan" || len(snapshot.Tasks) != 1 || snapshot.Tasks[0].Status != "completed" {
		t.Fatalf("unexpected new run snapshot: %#v", snapshot)
	}
}

func TestPlanUpdateTaskRewritesPlanTaskSnapshot(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{cfg: config.Config{Paths: config.PathsConfig{ChatsDir: root}}}
	execCtx := &ExecutionContext{
		Session: QuerySession{
			RunID:  "run_tasks",
			ChatID: "chat_1",
		},
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
		t.Fatalf("invoke plan_update_task: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected plan_update_task success, got %#v", result)
	}

	snapshot := readPlanTasksSnapshotForTest(t, root, "chat_1", "run_tasks")
	if snapshot.CurrentTaskID != "task_1" || len(snapshot.Tasks) != 1 ||
		snapshot.Tasks[0].Status != "in_progress" || snapshot.Tasks[0].Description != "new description" {
		t.Fatalf("unexpected snapshot after update: %#v", snapshot)
	}
}

func TestPlanTaskSnapshotsAreSeparatedByRun(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{cfg: config.Config{Paths: config.PathsConfig{ChatsDir: root}}}
	for _, runID := range []string{"run_alpha", "run_beta"} {
		execCtx := &ExecutionContext{
			Session: QuerySession{
				RunID:  runID,
				ChatID: "chat_1",
			},
		}
		result, err := executor.Invoke(context.Background(), PlanAddTasksToolName, map[string]any{
			"tasks": []any{map[string]any{"taskId": runID + "_task", "description": runID + " task"}},
		}, execCtx)
		if err != nil {
			t.Fatalf("invoke plan_add_tasks %s: %v", runID, err)
		}
		if result.ExitCode != 0 {
			t.Fatalf("expected plan_add_tasks success for %s, got %#v", runID, result)
		}
	}

	alpha := readPlanTasksSnapshotForTest(t, root, "chat_1", "run_alpha")
	beta := readPlanTasksSnapshotForTest(t, root, "chat_1", "run_beta")
	if alpha.PlanID != "run_alpha_plan" || beta.PlanID != "run_beta_plan" ||
		alpha.Tasks[0].TaskID != "run_alpha_task" || beta.Tasks[0].TaskID != "run_beta_task" {
		t.Fatalf("unexpected separated snapshots: alpha=%#v beta=%#v", alpha, beta)
	}
}

func TestPlanTaskSnapshotUsesRuntimeContextChatsDirFallback(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{}
	execCtx := &ExecutionContext{
		Session: QuerySession{
			RunID:  "run_tasks",
			ChatID: "chat_1",
			RuntimeContext: RuntimeRequestContext{
				LocalPaths: LocalPaths{ChatsDir: root},
			},
		},
	}

	result, err := executor.Invoke(context.Background(), PlanAddTasksToolName, map[string]any{
		"tasks": []any{map[string]any{"taskId": "task_1", "description": "first task"}},
	}, execCtx)
	if err != nil {
		t.Fatalf("invoke plan_add_tasks: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected plan_add_tasks success, got %#v", result)
	}
	snapshot := readPlanTasksSnapshotForTest(t, root, "chat_1", "run_tasks")
	if snapshot.PlanID != "run_tasks_plan" || len(snapshot.Tasks) != 1 {
		t.Fatalf("unexpected fallback snapshot: %#v", snapshot)
	}
}

func TestPlanTaskSnapshotPersistenceSkipsInvalidContextWithoutFailingTool(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{cfg: config.Config{Paths: config.PathsConfig{ChatsDir: root}}}
	execCtx := &ExecutionContext{
		Session: QuerySession{
			RunID:  "run_tasks",
			ChatID: "../bad-chat",
		},
	}

	result, err := executor.Invoke(context.Background(), PlanAddTasksToolName, map[string]any{
		"tasks": []any{map[string]any{"taskId": "task_1", "description": "first task"}},
	}, execCtx)
	if err != nil {
		t.Fatalf("invoke plan_add_tasks: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected plan_add_tasks success despite invalid persistence context, got %#v", result)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read temp chats root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected invalid chat id to skip snapshot write, got entries=%#v", entries)
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

func readPlanTasksSnapshotForTest(t *testing.T, root string, chatID string, runID string) planTasksSnapshot {
	t.Helper()
	path := filepath.Join(root, chatID, chat.ToolRootDirName, chat.ToolPlanTasksDirName, runID+"_plan.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read plan tasks snapshot %s: %v", path, err)
	}
	var snapshot planTasksSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatalf("decode plan tasks snapshot %s: %v", path, err)
	}
	return snapshot
}

func writePlanTasksSnapshotForTest(t *testing.T, root string, chatID string, runID string, content string) {
	t.Helper()
	dir := filepath.Join(root, chatID, chat.ToolRootDirName, chat.ToolPlanTasksDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir plan tasks snapshot dir: %v", err)
	}
	path := filepath.Join(dir, runID+"_plan.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write plan tasks snapshot %s: %v", path, err)
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
