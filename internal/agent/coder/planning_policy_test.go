package coder

import (
	"reflect"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

func TestCoderModeGuardsKeepProxySeparate(t *testing.T) {
	if !IsMode(" coder ") {
		t.Fatalf("expected CODER mode match to be case-insensitive")
	}
	if IsMode("PROXY") {
		t.Fatalf("ordinary PROXY mode must not be treated as CODER")
	}
	if !IsNativeBackend("CODER", "") {
		t.Fatalf("expected CODER without acpBridgeId to be native backend")
	}
	if IsNativeBackend("CODER", "codex") {
		t.Fatalf("expected CODER with acpBridgeId not to be native backend")
	}
	if !IsACPBackend("CODER", "codex") {
		t.Fatalf("expected CODER with acpBridgeId to be ACP backend")
	}
	if IsACPBackend("PROXY", "codex") {
		t.Fatalf("ordinary PROXY must not become ACP CODER")
	}
	if !PlanningModeEnabled("CODER", true) {
		t.Fatalf("expected requested planning mode to be enabled for CODER")
	}
	if PlanningModeEnabled("PROXY", true) {
		t.Fatalf("ordinary PROXY must not enter CODER planning mode")
	}
	if PlanningModeEnabled("CODER", false) {
		t.Fatalf("planning mode should require an explicit request")
	}
}

func TestCoderRuntimeToolNamesOnlyNativeExecuteAddsPlanTaskTools(t *testing.T) {
	base := []string{"bash", "file_read", contracts.PlanAddTasksToolName}
	wantNative := []string{
		"bash",
		"file_read",
		contracts.PlanAddTasksToolName,
		contracts.PlanGetTasksToolName,
		contracts.PlanUpdateTaskToolName,
	}
	if got := RuntimeToolNamesForAgent("CODER", "", "coder-execute", base); !reflect.DeepEqual(got, wantNative) {
		t.Fatalf("native CODER execute tools=%#v want %#v", got, wantNative)
	}
	if got := RuntimeToolNamesForAgent("CODER", "", "coder-plan", base); !reflect.DeepEqual(got, base) {
		t.Fatalf("native CODER plan tools=%#v want %#v", got, base)
	}
	if got := RuntimeToolNamesForAgent("CODER", "codex", "coder-execute", base); !reflect.DeepEqual(got, base) {
		t.Fatalf("ACP CODER execute tools=%#v want %#v", got, base)
	}
	if got := RuntimeToolNamesForAgent("PROXY", "", "coder-execute", base); !reflect.DeepEqual(got, base) {
		t.Fatalf("ordinary PROXY execute tools=%#v want %#v", got, base)
	}
}

func TestPlanningExecuteToolsFilterPlanningOnlyTools(t *testing.T) {
	base := []string{
		"bash",
		contracts.FinalizePlanningToolName,
		AskUserQuestionToolName,
		"file_read",
		contracts.PlanGetTasksToolName,
	}
	want := []string{
		"bash",
		"file_read",
		contracts.PlanGetTasksToolName,
		contracts.PlanAddTasksToolName,
		contracts.PlanUpdateTaskToolName,
	}
	if got := PlanningExecuteTools(base); !reflect.DeepEqual(got, want) {
		t.Fatalf("PlanningExecuteTools()=%#v want %#v", got, want)
	}
	if !IsPlanningOnlyTool(contracts.FinalizePlanningToolName) || !IsPlanningOnlyTool(AskUserQuestionToolName) {
		t.Fatalf("expected finalize_planning and ask_user_question to be planning-only")
	}
	if IsPlanningOnlyTool("bash") {
		t.Fatalf("bash must be available outside planning")
	}
}

func TestPlanningSystemInitSpecsPreserveCoderStagesAndTools(t *testing.T) {
	session := contracts.QuerySession{
		Mode:      "CODER",
		ToolNames: []string{"bash", "file_read", contracts.FinalizePlanningToolName, AskUserQuestionToolName},
	}
	settings := contracts.PlanExecuteSettings{
		Execute: contracts.StageSettings{
			SystemPrompt: "execute {{agent_key}}",
			Tools:        []string{"bash", "file_read", contracts.FinalizePlanningToolName, AskUserQuestionToolName},
		},
	}
	specs := PlanningSystemInitSpecs(session, api.QueryRequest{Message: "ship it"}, settings)
	if len(specs) != 2 {
		t.Fatalf("expected plan and execute specs, got %#v", specs)
	}

	plan := specs[0]
	if plan.CacheStage != "coder-plan" || plan.FingerprintStage != "coder-plan" ||
		plan.PromptStage != "coder-plan" || plan.Mode != "coder" || plan.Stage != "plan" {
		t.Fatalf("unexpected plan spec stages: %#v", plan)
	}
	if !plan.UseSharedSystemPrompt || !plan.IncludeAfterCallHints {
		t.Fatalf("plan spec should use shared system prompt and after-call hints: %#v", plan)
	}
	if !reflect.DeepEqual(plan.ToolNames, PlanningModePlanTools()) {
		t.Fatalf("plan spec tools=%#v want %#v", plan.ToolNames, PlanningModePlanTools())
	}

	execute := specs[1]
	wantExecuteTools := []string{
		"bash",
		"file_read",
		contracts.PlanAddTasksToolName,
		contracts.PlanGetTasksToolName,
		contracts.PlanUpdateTaskToolName,
	}
	if execute.CacheStage != "coder-execute" || execute.FingerprintStage != "coder-execute" ||
		execute.PromptStage != "coder-execute" || execute.Mode != "coder" || execute.Stage != "execute" {
		t.Fatalf("unexpected execute spec stages: %#v", execute)
	}
	if execute.UseSharedSystemPrompt || execute.IncludeAfterCallHints {
		t.Fatalf("execute spec should carry its own rendered prompt without after-call hints: %#v", execute)
	}
	if !reflect.DeepEqual(execute.ToolNames, wantExecuteTools) {
		t.Fatalf("execute spec tools=%#v want %#v", execute.ToolNames, wantExecuteTools)
	}
	if execute.SystemPrompt == "" {
		t.Fatalf("execute spec should include rendered execution system prompt")
	}
}
