package planmode

import (
	"reflect"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

func containsTool(tools []string, want string) bool {
	for _, tool := range tools {
		if tool == want {
			return true
		}
	}
	return false
}

func TestPlanningToolsRemoveConfiguredExclusionsAndAddFinalize(t *testing.T) {
	session := contracts.QuerySession{
		Mode:                 "CODER",
		ToolNames:            []string{"bash", "file_read", "web_fetch", AskUserQuestionToolName, "mcp_search", contracts.FinalizePlanningToolName},
		PlanningExcludeTools: []string{"bash", "file_write"},
	}
	want := []string{"file_read", "web_fetch", AskUserQuestionToolName, "mcp_search", contracts.FinalizePlanningToolName}
	if got := PlanningTools(session, nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("PlanningTools()=%#v want %#v", got, want)
	}
	session.PlanningExcludeTools = nil
	if got := PlanningTools(session, nil); !containsTool(got, "bash") {
		t.Fatalf("an empty exclusion list must not remove tools, got %#v", got)
	}
}

func TestPlanningToolsExcludeToolListedByDefinitionKey(t *testing.T) {
	session := contracts.QuerySession{
		ToolNames:            []string{"_bash_", "file_read"},
		PlanningExcludeTools: []string{"bash"},
	}
	defs := []api.ToolDetailResponse{{Name: "bash", Key: "_bash_"}, {Name: "file_read", Key: "file_read"}}
	want := []string{"file_read", contracts.FinalizePlanningToolName}
	if got := PlanningTools(session, defs); !reflect.DeepEqual(got, want) {
		t.Fatalf("PlanningTools()=%#v want %#v", got, want)
	}
}

func TestConfirmedPlanToolsRemoveConfiguredExclusions(t *testing.T) {
	session := contracts.QuerySession{
		Mode:                    "CODER",
		ToolNames:               []string{"bash", contracts.FinalizePlanningToolName, AskUserQuestionToolName, "file_read", contracts.PlanGetTasksToolName},
		PlanExecuteExcludeTools: []string{AskUserQuestionToolName},
	}
	want := []string{"bash", "file_read", contracts.PlanGetTasksToolName}
	if got := ConfirmedPlanTools(session, nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("ConfirmedPlanTools()=%#v want %#v", got, want)
	}
	session.PlanExecuteExcludeTools = nil
	if got := ConfirmedPlanTools(session, nil); !containsTool(got, AskUserQuestionToolName) || containsTool(got, contracts.FinalizePlanningToolName) {
		t.Fatalf("only configured tools and finalize_planning may be removed, got %#v", got)
	}
}

func TestToolSelectionIsTheSameForEveryMode(t *testing.T) {
	for _, mode := range []string{"GENERAL", "CODER", "KBASE"} {
		session := contracts.QuerySession{
			Mode:                    mode,
			ToolNames:               []string{"bash", "file_read", AskUserQuestionToolName},
			PlanningExcludeTools:    []string{"bash"},
			PlanExecuteExcludeTools: []string{AskUserQuestionToolName},
		}
		if got, want := PlanningTools(session, nil), []string{"file_read", AskUserQuestionToolName, contracts.FinalizePlanningToolName}; !reflect.DeepEqual(got, want) {
			t.Fatalf("%s PlanningTools()=%#v want %#v", mode, got, want)
		}
		if got, want := ConfirmedPlanTools(session, nil), []string{"bash", "file_read"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("%s ConfirmedPlanTools()=%#v want %#v", mode, got, want)
		}
	}
}
