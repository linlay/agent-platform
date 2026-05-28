package llm

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	contracts "agent-platform/internal/contracts"
)

func TestResolveAgentModeCoder(t *testing.T) {
	if _, ok := resolveAgentMode("CODER").(coderMode); !ok {
		t.Fatalf("expected CODER to resolve to coderMode")
	}
}

func TestCoderPlanningStageToolsAreReadOnlyPlusQuestionsAndPlan(t *testing.T) {
	stream := &coderPlanningStream{}
	want := []string{"file_read", "file_grep", "datetime", "ask_user_question", "planning_write"}
	if got := stream.planStageTools(); !reflect.DeepEqual(got, want) {
		t.Fatalf("planStageTools()=%#v want %#v", got, want)
	}
}

func TestCoderExecuteStageToolsExcludePlanningOnlyTools(t *testing.T) {
	stream := &coderPlanningStream{
		session: contracts.QuerySession{
			ToolNames: []string{"bash", "file_read", "plan_add_tasks", "planning_write", "ask_user_question", "plan_update_task", "datetime"},
		},
	}
	want := []string{"bash", "file_read", "datetime"}
	if got := stream.executeStageTools(); !reflect.DeepEqual(got, want) {
		t.Fatalf("executeStageTools()=%#v want %#v", got, want)
	}
}

func TestCoderPlanningPromptUsesCoderPromptsConfig(t *testing.T) {
	stream := &coderPlanningStream{
		engine: &LLMAgentEngine{cfg: config.Config{
			CoderPrompts: config.CoderPromptsConfig{
				PlanningPrompt: "custom coder planning prompt\nUse planning_write.",
			},
		}},
	}
	prompt := stream.planningPrompt()
	if !strings.Contains(prompt, "custom coder planning prompt") {
		t.Fatalf("expected custom coder planning prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "Use planning_write.") {
		t.Fatalf("expected configured planning_write instructions, got %q", prompt)
	}
}

func TestCoderSummaryPromptUsesCoderPromptsConfig(t *testing.T) {
	stream := &coderPlanningStream{
		engine: &LLMAgentEngine{cfg: config.Config{
			CoderPrompts: config.CoderPromptsConfig{
				SummaryUserPromptTemplate: "custom summary {{original_request}} {{confirmed_plan}}",
			},
		}},
		req: api.QueryRequest{Message: "build it"},
	}
	got := stream.renderSummaryUserPrompt("confirmed markdown")
	if got != "custom summary build it confirmed markdown" {
		t.Fatalf("expected custom coder summary prompt, got %q", got)
	}
}

func TestCoderPlanningConfirmationUsesPlanMode(t *testing.T) {
	stream := &coderPlanningStream{
		session: contracts.QuerySession{RunID: "run_1"},
		execCtx: &contracts.ExecutionContext{
			PlanningRevision: 1,
			PlanningState: &contracts.PlanningRuntimeState{
				PlanningID: "run_1_planning_1",
			},
			Budget: contracts.Budget{
				Tool: contracts.RetryPolicy{TimeoutMs: 120000},
				Hitl: contracts.HitlPolicy{TimeoutMs: 600000},
			},
		},
	}
	ask := stream.planConfirmationAsk()
	if ask.AwaitingID != "run_1_coder_plan_confirm_1" || ask.Mode != "plan" || ask.ViewportType != "builtin" || ask.ViewportKey != "plan" {
		t.Fatalf("expected plan confirmation ask, got %#v", ask)
	}
	if ask.Timeout != 0 {
		t.Fatalf("expected planning confirmation to wait forever with timeout 0, got %#v", ask)
	}
	if len(ask.Questions) != 0 || len(ask.Approvals) != 0 || len(ask.Plan) == 0 {
		t.Fatalf("expected one plan and no questions/approvals, got %#v", ask)
	}
	if ask.Plan["id"] != "confirm" || ask.Plan["planningId"] != "run_1_planning_1" || ask.Plan["title"] != "实施此计划？" {
		t.Fatalf("unexpected plan item %#v", ask.Plan)
	}
	options, _ := ask.Plan["options"].([]any)
	if len(options) != 2 {
		t.Fatalf("expected approve/reject options, got %#v", ask.Plan)
	}
	first, _ := options[0].(map[string]any)
	second, _ := options[1].(map[string]any)
	if first["label"] != "是，实施此计划" || first["decision"] != "approve" || second["label"] != "否，请告知如何调整" || second["decision"] != "reject" {
		t.Fatalf("expected explicit plan decisions, got %#v", options)
	}
	input, _ := second["input"].(map[string]any)
	if input["placeholder"] != "请告知如何调整" {
		t.Fatalf("expected reject feedback input, got %#v", second)
	}
}

func TestCoderPlanningConfirmationWaitsWithoutDisconnectedTimeout(t *testing.T) {
	runControl := contracts.NewRunControl(context.Background(), "run_1")
	runControl.SetMaxDisconnectedWait(20 * time.Millisecond)
	runControl.SetObserverCount(0)
	stream := &coderPlanningStream{
		session: contracts.QuerySession{RunID: "run_1"},
		execCtx: &contracts.ExecutionContext{
			RunControl:       runControl,
			PlanningRevision: 1,
		},
	}
	stream.emitPlanConfirmationAsk()

	resultCh := make(chan contracts.SubmitResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := runControl.AwaitSubmitWithTimeout(context.Background(), "run_1_coder_plan_confirm_1", 0)
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	select {
	case err := <-errCh:
		t.Fatalf("did not expect planning confirmation to expire while disconnected: %v", err)
	case result := <-resultCh:
		t.Fatalf("did not expect planning confirmation to resolve before submit: %#v", result)
	case <-time.After(80 * time.Millisecond):
	}

	params, err := api.EncodeSubmitParams([]map[string]any{{"id": "confirm", "decision": "approve"}})
	if err != nil {
		t.Fatalf("encode submit params: %v", err)
	}
	ack := runControl.ResolveSubmit(api.SubmitRequest{
		RunID:      "run_1",
		AwaitingID: "run_1_coder_plan_confirm_1",
		Params:     params,
	})
	if !ack.Accepted {
		t.Fatalf("expected planning confirmation submit to be accepted, got %#v", ack)
	}
	select {
	case err := <-errCh:
		t.Fatalf("expected submit result, got err %v", err)
	case result := <-resultCh:
		if result.Request.AwaitingID != "run_1_coder_plan_confirm_1" {
			t.Fatalf("unexpected result: %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for planning confirmation submit")
	}
}

func TestAwaitItemCountPlan(t *testing.T) {
	if got := awaitItemCount("plan", nil, nil, nil, map[string]any{"id": "confirm"}); got != 1 {
		t.Fatalf("plan item count = %d, want 1", got)
	}
	if got := awaitItemCount("plan", nil, nil, nil, nil); got != 0 {
		t.Fatalf("empty plan item count = %d, want 0", got)
	}
}
