package llm

import (
	"context"
	"reflect"
	"testing"
	"time"

	"agent-platform/internal/api"
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

func TestCoderPlanningConfirmationUsesApprovalMode(t *testing.T) {
	stream := &coderPlanningStream{
		session: contracts.QuerySession{RunID: "run_1"},
		execCtx: &contracts.ExecutionContext{
			Budget: contracts.Budget{
				Tool: contracts.RetryPolicy{TimeoutMs: 120000},
				Hitl: contracts.HitlPolicy{TimeoutMs: 600000},
			},
		},
	}
	ask := stream.planConfirmationAsk()
	if ask.Mode != "approval" || ask.ViewportType != "builtin" || ask.ViewportKey != "approval" {
		t.Fatalf("expected approval confirmation ask, got %#v", ask)
	}
	if ask.Timeout != 0 {
		t.Fatalf("expected planning confirmation to wait forever with timeout 0, got %#v", ask)
	}
	if len(ask.Questions) != 0 || len(ask.Approvals) != 1 {
		t.Fatalf("expected one approval and no questions, got %#v", ask)
	}
	approval, _ := ask.Approvals[0].(map[string]any)
	if approval["id"] != "confirm" {
		t.Fatalf("unexpected approval item %#v", approval)
	}
	options, _ := approval["options"].([]any)
	if len(options) != 2 {
		t.Fatalf("expected approve/reject options, got %#v", approval)
	}
	first, _ := options[0].(map[string]any)
	second, _ := options[1].(map[string]any)
	if first["decision"] != "approve" || second["decision"] != "reject" {
		t.Fatalf("expected explicit approval decisions, got %#v", options)
	}
}

func TestCoderPlanningConfirmationWaitsWithoutDisconnectedTimeout(t *testing.T) {
	runControl := contracts.NewRunControl(context.Background(), "run_1")
	runControl.SetMaxDisconnectedWait(20 * time.Millisecond)
	runControl.SetObserverCount(0)
	stream := &coderPlanningStream{
		session: contracts.QuerySession{RunID: "run_1"},
		execCtx: &contracts.ExecutionContext{
			RunControl: runControl,
		},
	}
	stream.emitPlanConfirmationAsk()

	resultCh := make(chan contracts.SubmitResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := runControl.AwaitSubmitWithTimeout(context.Background(), "run_1_coder_plan_confirm", 0)
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
		AwaitingID: "run_1_coder_plan_confirm",
		Params:     params,
	})
	if !ack.Accepted {
		t.Fatalf("expected planning confirmation submit to be accepted, got %#v", ack)
	}
	select {
	case err := <-errCh:
		t.Fatalf("expected submit result, got err %v", err)
	case result := <-resultCh:
		if result.Request.AwaitingID != "run_1_coder_plan_confirm" {
			t.Fatalf("unexpected result: %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for planning confirmation submit")
	}
}
