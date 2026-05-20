package llm

import (
	"reflect"
	"testing"

	contracts "agent-platform/internal/contracts"
)

func TestCoderPlanningStageToolsAreReadOnlyPlusQuestionsAndPlan(t *testing.T) {
	stream := &coderPlanningStream{}
	want := []string{"file_read", "file_grep", "datetime", "ask_user_question", "plan_add_tasks"}
	if got := stream.planStageTools(); !reflect.DeepEqual(got, want) {
		t.Fatalf("planStageTools()=%#v want %#v", got, want)
	}
}

func TestCoderExecuteStageToolsExcludePlanAddAndAppendPlanUpdate(t *testing.T) {
	stream := &coderPlanningStream{
		session: contracts.QuerySession{
			ToolNames: []string{"bash", "file_read", "plan_add_tasks", "datetime"},
		},
	}
	want := []string{"bash", "file_read", "datetime", "plan_update_task"}
	if got := stream.executeStageTools(); !reflect.DeepEqual(got, want) {
		t.Fatalf("executeStageTools()=%#v want %#v", got, want)
	}
}
