package plantasks

import (
	"reflect"
	"testing"

	"agent-platform/internal/apperrors"
	"agent-platform/internal/contracts"
)

func TestApplyTaskUpdateEnforcesOrderedSingleActiveTasks(t *testing.T) {
	state := &contracts.PlanRuntimeState{
		PlanID: "plan_1",
		Tasks: []contracts.PlanTask{
			{TaskID: "task_1", Description: "first", Status: "init"},
			{TaskID: "task_2", Description: "second", Status: "init"},
			{TaskID: "task_3", Description: "third", Status: "init"},
		},
	}

	if err := ApplyTaskUpdate(state, "task_1", "in_progress", ""); err != nil {
		t.Fatalf("start first task: %v", err)
	}
	if state.ActiveTaskID != "task_1" || state.Tasks[0].Status != "in_progress" {
		t.Fatalf("unexpected first task state: %#v", state)
	}

	before := clonePlanState(state)
	if err := ApplyTaskUpdate(state, "task_2", "completed", ""); err == nil || err.Code != apperrors.CodePlanTaskNotCurrent {
		t.Fatalf("complete future task error=%#v", err)
	}
	if !reflect.DeepEqual(state, before) {
		t.Fatalf("failed terminal update mutated state: before=%#v after=%#v", before, state)
	}

	if err := ApplyTaskUpdate(state, "task_3", "in_progress", ""); err == nil || err.Code != apperrors.CodePlanTaskPredecessorIncomplete || err.BlockingTaskID != "task_1" {
		t.Fatalf("start blocked future task error=%#v", err)
	}
	if !reflect.DeepEqual(state, before) {
		t.Fatalf("failed start update mutated state: before=%#v after=%#v", before, state)
	}

	if err := ApplyTaskUpdate(state, "task_1", "completed", ""); err != nil {
		t.Fatalf("complete first task: %v", err)
	}
	if err := ApplyTaskUpdate(state, "task_2", "in_progress", "updated second"); err != nil {
		t.Fatalf("start second task: %v", err)
	}
	if state.ActiveTaskID != "task_2" || state.Tasks[1].Description != "updated second" {
		t.Fatalf("unexpected second task state: %#v", state)
	}
}

func TestApplyTaskUpdateAllowsIdempotenceAndRejectsInvalidTransitions(t *testing.T) {
	state := &contracts.PlanRuntimeState{
		PlanID: "plan_1",
		Tasks: []contracts.PlanTask{
			{TaskID: "task_done", Description: "done", Status: "completed"},
			{TaskID: "task_active", Description: "active", Status: "in_progress"},
		},
		ActiveTaskID: "stale",
	}

	if err := ApplyTaskUpdate(state, "task_done", "completed", "renamed done"); err != nil {
		t.Fatalf("idempotent completed update: %v", err)
	}
	if state.Tasks[0].Description != "renamed done" || state.ActiveTaskID != "task_active" {
		t.Fatalf("idempotent update did not reconcile state: %#v", state)
	}

	before := clonePlanState(state)
	if err := ApplyTaskUpdate(state, "task_done", "in_progress", ""); err == nil || err.Code != apperrors.CodeInvalidPlanTaskTransition {
		t.Fatalf("revive terminal task error=%#v", err)
	}
	if !reflect.DeepEqual(state, before) {
		t.Fatalf("terminal revival mutated state: before=%#v after=%#v", before, state)
	}

	if err := ApplyTaskUpdate(state, "task_active", "init", ""); err == nil || err.Code != apperrors.CodeInvalidPlanTaskTransition {
		t.Fatalf("reset active task error=%#v", err)
	}
	if !reflect.DeepEqual(state, before) {
		t.Fatalf("active reset mutated state: before=%#v after=%#v", before, state)
	}
}

func TestApplyTaskUpdateAllowsEveryTerminalOutcomeAndThenNextTask(t *testing.T) {
	for _, terminalStatus := range []string{"completed", "failed", "canceled"} {
		t.Run(terminalStatus, func(t *testing.T) {
			state := &contracts.PlanRuntimeState{Tasks: []contracts.PlanTask{
				{TaskID: "task_1", Description: "first", Status: "init"},
				{TaskID: "task_2", Description: "second", Status: "init"},
			}}
			if err := ApplyTaskUpdate(state, "task_1", "init", "renamed first"); err != nil {
				t.Fatalf("idempotent init: %v", err)
			}
			if err := ApplyTaskUpdate(state, "task_1", "in_progress", ""); err != nil {
				t.Fatalf("start first: %v", err)
			}
			if err := ApplyTaskUpdate(state, "task_1", "in_progress", "active first"); err != nil {
				t.Fatalf("idempotent in_progress: %v", err)
			}
			if err := ApplyTaskUpdate(state, "task_1", terminalStatus, ""); err != nil {
				t.Fatalf("finish first as %s: %v", terminalStatus, err)
			}
			if err := ApplyTaskUpdate(state, "task_2", "in_progress", ""); err != nil {
				t.Fatalf("start second after %s: %v", terminalStatus, err)
			}
			if state.ActiveTaskID != "task_2" || state.Tasks[0].Status != terminalStatus || state.Tasks[0].Description != "active first" {
				t.Fatalf("unexpected state after %s: %#v", terminalStatus, state)
			}
		})
	}
}

func TestApplyTaskUpdateAllowsFirstInitTaskToMoveDirectlyToEveryTerminalOutcome(t *testing.T) {
	for _, terminalStatus := range []string{"completed", "failed", "canceled"} {
		t.Run(terminalStatus, func(t *testing.T) {
			state := &contracts.PlanRuntimeState{Tasks: []contracts.PlanTask{
				{TaskID: "task_1", Description: "first", Status: "init"},
				{TaskID: "task_2", Description: "second", Status: "init"},
			}}

			if err := ApplyTaskUpdate(state, "task_1", terminalStatus, "finished first"); err != nil {
				t.Fatalf("finish first directly as %s: %v", terminalStatus, err)
			}
			if state.ActiveTaskID != "" || state.Tasks[0].Status != terminalStatus || state.Tasks[0].Description != "finished first" {
				t.Fatalf("unexpected direct terminal state after %s: %#v", terminalStatus, state)
			}
			if err := ApplyTaskUpdate(state, "task_2", "in_progress", ""); err != nil {
				t.Fatalf("start second after direct %s: %v", terminalStatus, err)
			}
			if state.ActiveTaskID != "task_2" || state.Tasks[1].Status != "in_progress" {
				t.Fatalf("unexpected next task state after %s: %#v", terminalStatus, state)
			}
		})
	}
}

func TestApplyTaskUpdateRejectsDirectTerminalUpdateBehindPendingPredecessor(t *testing.T) {
	state := &contracts.PlanRuntimeState{Tasks: []contracts.PlanTask{
		{TaskID: "task_1", Description: "first", Status: "init"},
		{TaskID: "task_2", Description: "second", Status: "init"},
	}}

	before := clonePlanState(state)
	err := ApplyTaskUpdate(state, "task_2", "completed", "")
	if err == nil || err.Code != apperrors.CodePlanTaskPredecessorIncomplete || err.BlockingTaskID != "task_1" {
		t.Fatalf("complete blocked future task error=%#v", err)
	}
	if !reflect.DeepEqual(state, before) {
		t.Fatalf("blocked direct terminal update mutated state: before=%#v after=%#v", before, state)
	}
}

func TestReconcileStateKeepsOnlyRunnableActiveTask(t *testing.T) {
	state := &contracts.PlanRuntimeState{
		ActiveTaskID: "wrong",
		Tasks: []contracts.PlanTask{
			{TaskID: "task_1", Status: "completed"},
			{TaskID: "task_2", Status: "in_progress"},
			{TaskID: "task_3", Status: "completed"},
			{TaskID: "task_4", Status: "in_progress"},
		},
	}

	ReconcileState(state)
	if state.ActiveTaskID != "task_2" || state.Tasks[1].Status != "in_progress" || state.Tasks[3].Status != "init" {
		t.Fatalf("unexpected reconciled state: %#v", state)
	}
}

func TestReconcileStateDemotesActiveTaskBehindPendingPredecessor(t *testing.T) {
	state := &contracts.PlanRuntimeState{
		ActiveTaskID: "task_2",
		Tasks: []contracts.PlanTask{
			{TaskID: "task_1", Status: "init"},
			{TaskID: "task_2", Status: "in_progress"},
		},
	}
	ReconcileState(state)
	if state.ActiveTaskID != "" || state.Tasks[0].Status != "init" || state.Tasks[1].Status != "init" {
		t.Fatalf("unexpected pending-predecessor normalization: %#v", state)
	}
}

func clonePlanState(state *contracts.PlanRuntimeState) *contracts.PlanRuntimeState {
	cloned := *state
	cloned.Tasks = append([]contracts.PlanTask(nil), state.Tasks...)
	return &cloned
}
