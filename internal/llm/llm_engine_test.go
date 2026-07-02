package llm

import (
	"context"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/plantasks"
)

func TestResolveMaxStepsUsesBudgetAndDefaults(t *testing.T) {
	engine := &LLMAgentEngine{
		cfg: config.Config{
			Defaults: config.DefaultsConfig{
				React: config.ReactDefaultsConfig{MaxSteps: 6},
			},
		},
	}

	if got := engine.resolveMaxSteps(contracts.QuerySession{
		Budget: map[string]any{"maxSteps": 24},
		ResolvedBudget: contracts.Budget{
			MaxSteps: 24,
		},
	}, "react"); got != 24 {
		t.Fatalf("resolveMaxSteps() = %d, want budget max steps 24", got)
	}
	if got := engine.resolveMaxSteps(contracts.QuerySession{}, "react"); got != 100 {
		t.Fatalf("resolveMaxSteps() = %d, want budget default 100", got)
	}
}

func TestNewRunStreamRequiresExplicitModelKey(t *testing.T) {
	engine := &LLMAgentEngine{}

	_, err := engine.newRunStreamWithOptions(context.Background(), api.QueryRequest{}, contracts.QuerySession{}, true, runStreamOptions{Stage: "react"})
	if err == nil || !strings.Contains(err.Error(), "modelConfig.modelKey is required") {
		t.Fatalf("expected explicit model key error, got %v", err)
	}
}

func TestRestorePlanTasksForRunLoadsSnapshotForPlanTools(t *testing.T) {
	root := t.TempDir()
	if _, err := plantasks.PersistState(root, "chat_1", "run_old", &contracts.PlanRuntimeState{
		PlanID:       "old_plan",
		ActiveTaskID: "task_1",
		Tasks: []contracts.PlanTask{{
			TaskID:      "task_1",
			Description: "resume this",
			Status:      "in_progress",
		}},
	}); err != nil {
		t.Fatalf("persist plan state: %v", err)
	}
	engine := &LLMAgentEngine{cfg: config.Config{Paths: config.PathsConfig{ChatsDir: root}}}
	session := contracts.QuerySession{RunID: "run_new", ChatID: "chat_1"}
	execCtx := &contracts.ExecutionContext{Session: session}

	engine.restorePlanTasksForRun(execCtx, &session, "coder", []api.ToolDetailResponse{{Name: contracts.PlanGetTasksToolName}})

	if execCtx.PlanState == nil || execCtx.PlanState.PlanID != "old_plan" || execCtx.PlanState.ActiveTaskID != "task_1" {
		t.Fatalf("expected restored plan state, got %#v", execCtx.PlanState)
	}
	if !strings.Contains(session.PlanTaskContext, "Runtime Context: Current Plan Tasks") ||
		!strings.Contains(session.PlanTaskContext, "task_1 | in_progress | resume this") {
		t.Fatalf("expected plan task context, got %q", session.PlanTaskContext)
	}
	if execCtx.Session.PlanTaskContext != session.PlanTaskContext {
		t.Fatalf("expected execCtx session to receive plan task context, got %#v", execCtx.Session)
	}
}

func TestRestorePlanTasksForRunSkipsPlanningStage(t *testing.T) {
	root := t.TempDir()
	if _, err := plantasks.PersistState(root, "chat_1", "run_old", &contracts.PlanRuntimeState{
		PlanID: "old_plan",
		Tasks:  []contracts.PlanTask{{TaskID: "task_1", Description: "old", Status: "init"}},
	}); err != nil {
		t.Fatalf("persist plan state: %v", err)
	}
	engine := &LLMAgentEngine{cfg: config.Config{Paths: config.PathsConfig{ChatsDir: root}}}
	session := contracts.QuerySession{RunID: "run_new", ChatID: "chat_1"}
	execCtx := &contracts.ExecutionContext{Session: session}

	engine.restorePlanTasksForRun(execCtx, &session, "coder-plan", []api.ToolDetailResponse{{Name: contracts.PlanGetTasksToolName}})

	if execCtx.PlanState != nil {
		t.Fatalf("planning stage should not restore plan tasks, got %#v", execCtx.PlanState)
	}
	if session.PlanTaskContext != "" {
		t.Fatalf("planning stage should not set plan context, got %q", session.PlanTaskContext)
	}
}
