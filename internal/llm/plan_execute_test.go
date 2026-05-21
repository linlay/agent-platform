package llm

import (
	"reflect"
	"testing"

	contracts "agent-platform/internal/contracts"
)

func TestPlanStageToolsDefaultsToPlanAddTasksOnly(t *testing.T) {
	stream := &planExecuteStream{
		session: contracts.QuerySession{
			ToolNames: []string{"datetime", "memory_search"},
		},
	}

	if got, want := stream.planStageTools(), []string{"plan_add_tasks"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("planStageTools()=%#v want %#v", got, want)
	}
}

func TestPlanStageToolsPreservesExplicitPlanToolsWithoutSessionFallback(t *testing.T) {
	stream := &planExecuteStream{
		session: contracts.QuerySession{
			ToolNames: []string{"memory_search", "datetime"},
		},
		settings: contracts.PlanExecuteSettings{
			Plan: contracts.StageSettings{
				Tools: []string{"mock.plan.inspect", "datetime"},
			},
		},
	}

	if got, want := stream.planStageTools(), []string{"mock.plan.inspect", "datetime", "plan_add_tasks"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("planStageTools()=%#v want %#v", got, want)
	}
}

func TestPlanStagePostToolHookStopsAfterTasksCreated(t *testing.T) {
	stream := &planExecuteStream{
		execCtx: &contracts.ExecutionContext{
			PlanState: &contracts.PlanRuntimeState{},
		},
	}

	if got := stream.planStagePostToolHook("datetime", "tool_1"); got != PostToolContinue {
		t.Fatalf("non-plan tool hook=%v want %v", got, PostToolContinue)
	}
	if got := stream.planStagePostToolHook("plan_add_tasks", "tool_1"); got != PostToolContinue {
		t.Fatalf("empty plan hook=%v want %v", got, PostToolContinue)
	}

	stream.execCtx.PlanState.Tasks = []contracts.PlanTask{{TaskID: "task_1", Description: "first task"}}
	if got := stream.planStagePostToolHook("plan_add_tasks", "tool_1"); got != PostToolStop {
		t.Fatalf("created-plan hook=%v want %v", got, PostToolStop)
	}
}

func TestNonSystemMessagesFiltersSystemMessages(t *testing.T) {
	messages := []openAIMessage{
		{Role: "system", Content: "first system"},
		{Role: "user", Content: "first user"},
		{Role: " system ", Content: "spaced system"},
		{Role: "assistant", Content: "assistant reply"},
		{Role: "", Content: "empty role"},
	}

	got := nonSystemMessages(messages)
	want := []openAIMessage{
		{Role: "user", Content: "first user"},
		{Role: "assistant", Content: "assistant reply"},
		{Role: "", Content: "empty role"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nonSystemMessages()=%#v want %#v", got, want)
	}
}
