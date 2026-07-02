package plantasks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStateFromSnapshotNormalizesTasksAndActiveTask(t *testing.T) {
	state := StateFromSnapshot(&Snapshot{
		RunID:         "run_old",
		PlanID:        "plan_old",
		CurrentTaskID: "task_active",
		Tasks: []TaskSnapshot{
			{TaskID: " task_done ", Description: " done ", Status: "complete"},
			{TaskID: "task_active", Description: "active", Status: "in-progress"},
			{TaskID: "task_bad", Description: "bad", Status: "unknown"},
		},
	})
	if state == nil {
		t.Fatal("expected restored state")
	}
	if state.PlanID != "plan_old" || state.ActiveTaskID != "task_active" {
		t.Fatalf("unexpected state metadata: %#v", state)
	}
	if len(state.Tasks) != 2 {
		t.Fatalf("expected invalid task to be skipped, got %#v", state.Tasks)
	}
	if state.Tasks[0].TaskID != "task_done" || state.Tasks[0].Status != "completed" || state.Tasks[0].Description != "done" {
		t.Fatalf("unexpected normalized completed task: %#v", state.Tasks[0])
	}
	if state.Tasks[1].Status != "in_progress" {
		t.Fatalf("unexpected active task status: %#v", state.Tasks[1])
	}

	state = StateFromSnapshot(&Snapshot{
		RunID:         "run_old",
		CurrentTaskID: "task_done",
		Tasks:         []TaskSnapshot{{TaskID: "task_done", Description: "done", Status: "completed"}},
	})
	if state == nil || state.PlanID != "run_old_plan" {
		t.Fatalf("expected fallback plan id, got %#v", state)
	}
	if state.ActiveTaskID != "" {
		t.Fatalf("terminal currentTaskId should be cleared, got %#v", state)
	}
}

func TestFormatStateContextIncludesPlanTasks(t *testing.T) {
	state := StateFromSnapshot(&Snapshot{
		PlanID:        "plan_old",
		CurrentTaskID: "task_active",
		Tasks: []TaskSnapshot{
			{TaskID: "task_active", Description: "active", Status: "in_progress"},
		},
	})
	context := FormatStateContext(state)
	for _, want := range []string{
		"Runtime Context: Current Plan Tasks",
		"planId: plan_old",
		"currentTaskId: task_active",
		"- task_active | in_progress | active",
	} {
		if !strings.Contains(context, want) {
			t.Fatalf("expected %q in context:\n%s", want, context)
		}
	}
}

func TestLoadLatestStateForChatSelectsNewestSnapshot(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "chat_1", ToolRootDirName, DirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir snapshot dir: %v", err)
	}
	writeSnapshotForTest(t, filepath.Join(dir, "run_old_plan.json"), `{"version":1,"chatId":"chat_1","runId":"run_old","planId":"old_plan","updatedAt":100,"tasks":[{"taskId":"old_task","description":"old","status":"init"}]}`)
	writeSnapshotForTest(t, filepath.Join(dir, "run_new_plan.json"), `{"version":1,"chatId":"chat_1","runId":"run_new","planId":"new_plan","updatedAt":200,"tasks":[{"taskId":"new_task","description":"new","status":"in_progress"}]}`)

	state, err := LoadLatestStateForChat(root, "chat_1")
	if err != nil {
		t.Fatalf("load latest state: %v", err)
	}
	if state == nil || state.PlanID != "new_plan" || len(state.Tasks) != 1 || state.Tasks[0].TaskID != "new_task" {
		t.Fatalf("unexpected latest state: %#v", state)
	}
}

func TestLoadLatestStateForChatSkipsInvalidChatID(t *testing.T) {
	state, err := LoadLatestStateForChat(t.TempDir(), "../chat")
	if err != nil {
		t.Fatalf("invalid chat id should not return error: %v", err)
	}
	if state != nil {
		t.Fatalf("expected invalid chat id to skip restore, got %#v", state)
	}
}

func writeSnapshotForTest(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write snapshot %s: %v", path, err)
	}
}
