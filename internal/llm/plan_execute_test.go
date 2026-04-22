package llm

import (
	"reflect"
	"testing"

	contracts "agent-platform-runner-go/internal/contracts"
)

func TestPlanStageToolsDefaultsToPlanAddTasksOnly(t *testing.T) {
	stream := &planExecuteStream{
		session: contracts.QuerySession{
			ToolNames: []string{"_datetime_", "_memory_search_"},
		},
	}

	if got, want := stream.planStageTools(), []string{"plan_add_tasks"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("planStageTools()=%#v want %#v", got, want)
	}
}

func TestPlanStageToolsPreservesExplicitPlanToolsWithoutSessionFallback(t *testing.T) {
	stream := &planExecuteStream{
		session: contracts.QuerySession{
			ToolNames: []string{"_memory_search_", "_datetime_"},
		},
		settings: contracts.PlanExecuteSettings{
			Plan: contracts.StageSettings{
				Tools: []string{"mock.plan.inspect", "_datetime_"},
			},
		},
	}

	if got, want := stream.planStageTools(), []string{"mock.plan.inspect", "_datetime_", "plan_add_tasks"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("planStageTools()=%#v want %#v", got, want)
	}
}

func TestPlanStagePostToolHookStopsAfterTasksCreated(t *testing.T) {
	stream := &planExecuteStream{
		execCtx: &contracts.ExecutionContext{
			PlanState: &contracts.PlanRuntimeState{},
		},
	}

	if got := stream.planStagePostToolHook("_datetime_", "tool_1"); got != PostToolContinue {
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
