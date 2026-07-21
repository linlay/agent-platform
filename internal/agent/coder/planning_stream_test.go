package coder

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/api"
	contracts "agent-platform/internal/contracts"
)

type fakePlanningRuntime struct {
	settings RuntimeSettings
	toolDefs []api.ToolDetailResponse
}

func (f fakePlanningRuntime) Settings() RuntimeSettings {
	return f.settings
}

func (f fakePlanningRuntime) NewStageRunStream(context.Context, api.QueryRequest, contracts.QuerySession, bool, StageRunOptions) (contracts.AgentStream, error) {
	return nil, nil
}

func (f fakePlanningRuntime) BuildCurrentMessagesForRequest(api.QueryRequest, contracts.QuerySession, bool) []map[string]any {
	return nil
}

func (f fakePlanningRuntime) ToolDefinitions() []api.ToolDetailResponse {
	return f.toolDefs
}

func (f fakePlanningRuntime) BuildExecuteSystemInitProfiles(contracts.QuerySession, api.QueryRequest, contracts.CoderPlanningSettings) []contracts.SystemInitProfile {
	return nil
}

func TestCoderPlanningStageToolsAreReadOnlyPlusVisionQuestionsAndFinalizePlanning(t *testing.T) {
	stream := &coderPlanningStream{}
	want := []string{"file_read", "file_glob", "file_grep", "datetime", "regex", "vision_recognize", "ask_user_question", contracts.FinalizePlanningToolName}
	if got := stream.planningStageTools(); !reflect.DeepEqual(got, want) {
		t.Fatalf("planningStageTools()=%#v want %#v", got, want)
	}
	forbidden := map[string]struct{}{
		"bash":             {},
		"file_write":       {},
		"file_edit":        {},
		"artifact_publish": {},
		"plan_add_tasks":   {},
		"plan_get_tasks":   {},
		"plan_update_task": {},
	}
	for _, tool := range stream.planningStageTools() {
		if _, ok := forbidden[tool]; ok {
			t.Fatalf("planningStageTools() must not include mutating tool %q: %#v", tool, stream.planningStageTools())
		}
	}
}

func TestCoderExecuteStageToolsIncludePlanTaskTools(t *testing.T) {
	stream := &coderPlanningStream{
		session: contracts.QuerySession{
			ToolNames: []string{"bash", "file_read", "artifact_publish", "plan_add_tasks", contracts.FinalizePlanningToolName, "ask_user_question", "plan_update_task", "datetime"},
		},
	}
	want := []string{"bash", "file_read", "artifact_publish", "plan_add_tasks", "plan_update_task", "datetime", "plan_get_tasks"}
	if got := stream.executeStageTools(); !reflect.DeepEqual(got, want) {
		t.Fatalf("executeStageTools()=%#v want %#v", got, want)
	}
}

func TestCoderPlanningPromptUsesCoderPromptsConfig(t *testing.T) {
	stream := &coderPlanningStream{
		runtime: fakePlanningRuntime{
			settings: RuntimeSettings{
				PlanningPrompt: "custom {{agent_key}} {{workspace_dir}} {{planning_stage_tools}} {{execute_stage_tools}}\nUse {{finalize_planning_tool_name}}.\n{{execute_tool_descriptions}}",
			},
		},
		session: contracts.QuerySession{
			AgentKey:     "coder",
			PlanningMode: true,
			ToolNames:    []string{"bash", "file_read", "file_write", "file_edit", "datetime"},
			RuntimeContext: contracts.RuntimeRequestContext{
				LocalPaths: contracts.LocalPaths{WorkspaceDir: "/workspace"},
			},
		},
	}
	prompt := stream.planningPrompt()
	if !strings.Contains(prompt, "custom coder /workspace") {
		t.Fatalf("expected custom coder planning prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "Use finalize_planning.") {
		t.Fatalf("expected configured finalize_planning instructions, got %q", prompt)
	}
	if !strings.Contains(prompt, "file_read, file_glob, file_grep, datetime, regex, vision_recognize, ask_user_question, finalize_planning") {
		t.Fatalf("expected rendered plan stage tools, got %q", prompt)
	}
	if !strings.Contains(prompt, "bash, file_read, file_write, file_edit, datetime, plan_add_tasks, plan_get_tasks, plan_update_task") {
		t.Fatalf("expected rendered execute stage tools, got %q", prompt)
	}
	if strings.Contains(prompt, "{{") || strings.Contains(prompt, "}}") {
		t.Fatalf("expected all configured CODER planning placeholders to be rendered, got %q", prompt)
	}
}

func TestCoderExecutionSystemPromptIncludesRenderedCoderSystemPrompt(t *testing.T) {
	stream := &coderPlanningStream{
		req: api.QueryRequest{Message: "build it"},
		session: contracts.QuerySession{
			AgentKey:         "coder",
			AgentName:        "Coder",
			Mode:             "CODER",
			PlanningMode:     true,
			ToolNames:        []string{"bash", "file_read", contracts.FinalizePlanningToolName, "ask_user_question"},
			ModeSystemPrompt: "CODER {{agent_key}} {{agent_name}} {{available_tools}} {{execute_stage_tools}} {{bash_tool_name}}",
		},
		settings: contracts.CoderPlanningSettings{
			Execute: contracts.StageSettings{
				SystemPrompt: "stage {{agent_key}} {{workspace_dir}}",
				Tools:        []string{"bash", "file_read"},
			},
		},
	}
	got := stream.executionSystemPrompt("fallback {{agent_key}}")
	for _, expected := range []string{
		"CODER coder Coder bash, file_read, plan_add_tasks, plan_get_tasks, plan_update_task bash, file_read, plan_add_tasks, plan_get_tasks, plan_update_task bash",
		"stage coder",
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("expected %q in rendered execution prompt, got %q", expected, got)
		}
	}
	if strings.Contains(got, "{{") || strings.Contains(got, "}}") {
		t.Fatalf("expected all configured CODER execution placeholders to be rendered, got %q", got)
	}
}

func TestPlanningApproveContinuationCarriesInMemoryAdmissionState(t *testing.T) {
	state := &struct{ key string }{key: "frozen"}
	stream := &coderPlanningStream{session: contracts.QuerySession{
		RunID:    "source-run",
		ChatID:   "chat",
		AgentKey: "coder",
		Locale:   "zh-CN",
	}}
	if !stream.preparePlanningApproveContinuation(api.SubmitRequest{
		ContinuationRunID: "execute-run",
		SubmitID:          "submit",
		Params:            api.SubmitParams{[]byte(`{"decision":"approve"}`)},
		ContinuationState: state,
	}, "await", map[string]any{"planning": map[string]any{"decision": "approve"}}) {
		t.Fatal("expected planning approval continuation")
	}
	if len(stream.pending) != 1 {
		t.Fatalf("pending deltas = %#v", stream.pending)
	}
	delta, ok := stream.pending[0].(contracts.DeltaRunContinuation)
	if !ok || delta.ContinuationState != state {
		t.Fatalf("continuation state was not preserved: %#v", stream.pending[0])
	}
}

func TestCoderPlanningExecutionEOFCompletesWithoutSummaryStage(t *testing.T) {
	stream := &coderPlanningStream{
		planningDone:     true,
		confirmationDone: true,
		executionDone:    false,
	}
	if err := stream.afterStageEOF(); err != nil {
		t.Fatalf("afterStageEOF: %v", err)
	}
	if !stream.executionDone || !stream.summaryDone || !stream.completed {
		t.Fatalf("expected execution EOF to complete the run, got %#v", stream)
	}
	if stream.current != nil {
		t.Fatalf("did not expect a summary stream to start, got %#v", stream.current)
	}
	if len(stream.pending) != 0 {
		t.Fatalf("did not expect summary stage marker after execution EOF, got %#v", stream.pending)
	}
}

func TestCoderPlanningConfirmationUsesPlanningMode(t *testing.T) {
	stream := &coderPlanningStream{
		session: contracts.QuerySession{RunID: "run_1"},
		execCtx: &contracts.ExecutionContext{
			PlanningRevision: 1,
			PlanningState: &contracts.PlanningRuntimeState{
				PlanningID:   "run_1_planning_1",
				PlanningFile: "/tmp/chat_1/.tools/planning/run_1_planning_1.md",
				ToolCallID:   "tool_plan",
				ToolName:     contracts.FinalizePlanningToolName,
			},
			Budget: contracts.Budget{
				Tool: contracts.RetryPolicy{Timeout: 120},
				Hitl: contracts.HitlPolicy{Timeout: 600},
			},
		},
	}
	ask := stream.planningConfirmationAsk()
	if ask.AwaitingID != "tool_plan" || ask.Mode != "planning" || ask.ViewportType != "builtin" || ask.ViewportKey != "planning" {
		t.Fatalf("expected planning confirmation ask, got %#v", ask)
	}
	if ask.Timeout != 0 {
		t.Fatalf("expected planning confirmation to have no configured timeout, got %#v", ask)
	}
	if len(ask.Questions) != 0 || len(ask.Approvals) != 0 || len(ask.Planning) == 0 {
		t.Fatalf("expected one planning and no questions/approvals, got %#v", ask)
	}
	if ask.Planning["id"] != "confirm" || ask.Planning["planningId"] != "run_1_planning_1" ||
		ask.Planning["planningFile"] != "/tmp/chat_1/.tools/planning/run_1_planning_1.md" {
		t.Fatalf("unexpected planning item %#v", ask.Planning)
	}
	if _, ok := ask.Planning["title"]; ok {
		t.Fatalf("did not expect builtin planning title in platform payload, got %#v", ask.Planning)
	}
	options, _ := ask.Planning["options"].([]any)
	if len(options) != 2 {
		t.Fatalf("expected approve/reject options, got %#v", ask.Planning)
	}
	first, _ := options[0].(map[string]any)
	second, _ := options[1].(map[string]any)
	if first["decision"] != "approve" || second["decision"] != "reject" {
		t.Fatalf("expected explicit plan decisions, got %#v", options)
	}
	for _, rawOption := range options {
		option, _ := rawOption.(map[string]any)
		if _, ok := option["label"]; ok {
			t.Fatalf("did not expect builtin plan option label in platform payload, got %#v", options)
		}
		if _, ok := option["input"]; ok {
			t.Fatalf("did not expect builtin plan option input in platform payload, got %#v", options)
		}
	}
}

func TestCoderPlanningStageEOFWithFinalizePlanningEmitsConfirmation(t *testing.T) {
	stream := &coderPlanningStream{
		session: contracts.QuerySession{RunID: "run_1"},
		execCtx: &contracts.ExecutionContext{
			PlanningRevision: 1,
			PlanningState: &contracts.PlanningRuntimeState{
				PlanningID: "run_1_planning_1",
				Markdown:   "# Plan\n\n- Do it",
				ToolCallID: "tool_plan",
				ToolName:   contracts.FinalizePlanningToolName,
			},
		},
	}
	if err := stream.afterStageEOF(); err != nil {
		t.Fatalf("afterStageEOF: %v", err)
	}
	if !stream.planningDone || stream.completed || stream.summaryDone || !stream.confirmationPending {
		t.Fatalf("unexpected planning stream state: %#v", stream)
	}
	if len(stream.pending) != 1 {
		t.Fatalf("expected one pending confirmation ask, got %#v", stream.pending)
	}
	ask, ok := stream.pending[0].(contracts.DeltaAwaitAsk)
	if !ok || ask.Mode != "planning" || ask.AwaitingID != "tool_plan" || ask.Planning["planningId"] != "run_1_planning_1" {
		t.Fatalf("expected planning confirmation ask, got %#v", stream.pending[0])
	}
}

func TestCoderPlanningStageEOFWithoutPlanButAssistantTextCompletes(t *testing.T) {
	stream := &coderPlanningStream{
		execCtx: &contracts.ExecutionContext{},
		executeMessages: []contracts.ModelMessage{
			{Role: "user", Content: "这个怎么产生的"},
			{Role: "assistant", Content: "这是由 planningMode 误触发产生的。"},
		},
	}
	if err := stream.afterStageEOF(); err != nil {
		t.Fatalf("afterStageEOF: %v", err)
	}
	if !stream.planningDone || !stream.completed || !stream.summaryDone {
		t.Fatalf("expected planning stream to complete normally, got %#v", stream)
	}
	for _, delta := range stream.pending {
		if errDelta, ok := delta.(contracts.DeltaError); ok {
			t.Fatalf("did not expect error for assistant text, got %#v", errDelta)
		}
	}
}

func TestCoderPlanningStageEOFWithoutPlanAndTextEmitsModelError(t *testing.T) {
	stream := &coderPlanningStream{
		execCtx: &contracts.ExecutionContext{},
		executeMessages: []contracts.ModelMessage{
			{Role: "assistant", ToolCalls: []contracts.ModelToolCall{{ID: "tool_1"}}},
		},
	}
	if err := stream.afterStageEOF(); err != nil {
		t.Fatalf("afterStageEOF: %v", err)
	}
	if !stream.planningDone || !stream.completed || !stream.summaryDone {
		t.Fatalf("expected planning stream to complete after error, got %#v", stream)
	}
	if len(stream.pending) != 1 {
		t.Fatalf("expected one pending error, got %#v", stream.pending)
	}
	errDelta, ok := stream.pending[0].(contracts.DeltaError)
	if !ok {
		t.Fatalf("expected DeltaError, got %#v", stream.pending[0])
	}
	if errDelta.Error["code"] != "planning_not_created" || errDelta.Error["category"] != "model" {
		t.Fatalf("expected model planning_not_created error, got %#v", errDelta.Error)
	}
}

func TestCoderPlanningFeedbackStageEOFWithoutPlanCompletes(t *testing.T) {
	stream := &coderPlanningStream{
		execCtx:                   &contracts.ExecutionContext{},
		currentPlanningIsFeedback: true,
	}
	if err := stream.afterStageEOF(); err != nil {
		t.Fatalf("afterStageEOF: %v", err)
	}
	if !stream.planningDone || !stream.completed || !stream.summaryDone {
		t.Fatalf("expected feedback stage to complete normally, got %#v", stream)
	}
	if len(stream.pending) != 0 {
		t.Fatalf("did not expect pending deltas for empty feedback, got %#v", stream.pending)
	}
}

func TestPlanningStageHasAssistantText(t *testing.T) {
	cases := []struct {
		name     string
		messages []contracts.ModelMessage
		want     bool
	}{
		{
			name:     "assistant string",
			messages: []contracts.ModelMessage{{Role: "assistant", Content: "请补充一下范围。"}},
			want:     true,
		},
		{
			name: "assistant content parts",
			messages: []contracts.ModelMessage{{
				Role: "assistant",
				Content: []any{
					map[string]any{"type": "text", "text": "已取消执行计划。"},
				},
			}},
			want: true,
		},
		{
			name:     "user text only",
			messages: []contracts.ModelMessage{{Role: "user", Content: "hello"}},
			want:     false,
		},
		{
			name:     "assistant tool call only",
			messages: []contracts.ModelMessage{{Role: "assistant", ToolCalls: []contracts.ModelToolCall{{ID: "tool_1"}}}},
			want:     false,
		},
		{
			name: "history assistant before current user",
			messages: []contracts.ModelMessage{
				{Role: "assistant", Content: "历史回复"},
				{Role: "user", Content: "当前请求"},
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := planningStageHasAssistantText(tc.messages); got != tc.want {
				t.Fatalf("planningStageHasAssistantText()=%v want %v", got, tc.want)
			}
		})
	}
}

func TestCoderPlanningConfirmationWaitsWithoutDisconnectedTimeout(t *testing.T) {
	runControl := contracts.NewRunControl(context.Background(), "run_1")
	runControl.SetObserverCount(0)
	stream := &coderPlanningStream{
		session: contracts.QuerySession{RunID: "run_1"},
		execCtx: &contracts.ExecutionContext{
			RunControl:       runControl,
			PlanningRevision: 1,
		},
	}
	stream.emitPlanningConfirmationAsk()

	resultCh := make(chan contracts.SubmitResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := runControl.AwaitSubmitIndefinitely(context.Background(), "run_1_coder_planning_confirm_1")
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
		AwaitingID: "run_1_coder_planning_confirm_1",
		Params:     params,
	})
	if !ack.Accepted {
		t.Fatalf("expected planning confirmation submit to be accepted, got %#v", ack)
	}
	select {
	case err := <-errCh:
		t.Fatalf("expected submit result, got err %v", err)
	case result := <-resultCh:
		if result.Request.AwaitingID != "run_1_coder_planning_confirm_1" {
			t.Fatalf("unexpected result: %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for planning confirmation submit")
	}
}

func TestCoderPlanningConfirmationPausesRunBudget(t *testing.T) {
	runControl := contracts.NewRunControl(context.Background(), "run_1")
	stream := &coderPlanningStream{
		session: contracts.QuerySession{RunID: "run_1"},
		execCtx: &contracts.ExecutionContext{
			RunControl:       runControl,
			PlanningRevision: 1,
			StartedAt:        time.Now().Add(-time.Second),
			Budget:           contracts.Budget{Timeout: 1, MaxSteps: 10},
		},
	}
	stream.emitPlanningConfirmationAsk()

	done := make(chan error, 1)
	go func() {
		done <- stream.awaitPlanningConfirmation()
	}()
	time.Sleep(25 * time.Millisecond)
	params, err := api.EncodeSubmitParams([]map[string]any{{"id": "confirm", "decision": "reject"}})
	if err != nil {
		t.Fatalf("encode submit params: %v", err)
	}
	ack := runControl.ResolveSubmit(api.SubmitRequest{
		RunID:      "run_1",
		AwaitingID: "run_1_coder_planning_confirm_1",
		Params:     params,
	})
	if !ack.Accepted {
		t.Fatalf("expected planning confirmation submit to be accepted, got %#v", ack)
	}
	if err := <-done; err != nil {
		t.Fatalf("await planning confirmation: %v", err)
	}
	if stream.execCtx.BudgetPaused < 20*time.Millisecond {
		t.Fatalf("expected planning confirmation wait to pause the run budget, got %s", stream.execCtx.BudgetPaused)
	}
}

func TestAwaitItemCountPlanning(t *testing.T) {
	if got := awaitItemCount("planning", nil, nil, nil, map[string]any{"id": "confirm"}); got != 1 {
		t.Fatalf("planning item count = %d, want 1", got)
	}
	if got := awaitItemCount("planning", nil, nil, nil, nil); got != 0 {
		t.Fatalf("empty planning item count = %d, want 0", got)
	}
}
