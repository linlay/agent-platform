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

func TestCoderPlanningStageToolsAreReadOnlyPlusVisionQuestionsAndPlan(t *testing.T) {
	stream := &coderPlanningStream{}
	want := []string{"file_read", "file_glob", "file_grep", "datetime", "regex", "vision_recognize", "ask_user_question", contracts.FinalizePlanningToolName}
	if got := stream.planStageTools(); !reflect.DeepEqual(got, want) {
		t.Fatalf("planStageTools()=%#v want %#v", got, want)
	}
	forbidden := map[string]struct{}{
		"bash":             {},
		"file_write":       {},
		"file_edit":        {},
		"plan_add_tasks":   {},
		"plan_get_tasks":   {},
		"plan_update_task": {},
	}
	for _, tool := range stream.planStageTools() {
		if _, ok := forbidden[tool]; ok {
			t.Fatalf("planStageTools() must not include mutating tool %q: %#v", tool, stream.planStageTools())
		}
	}
}

func TestCoderExecuteStageToolsIncludePlanTaskTools(t *testing.T) {
	stream := &coderPlanningStream{
		session: contracts.QuerySession{
			ToolNames: []string{"bash", "file_read", "plan_add_tasks", contracts.FinalizePlanningToolName, "ask_user_question", "plan_update_task", "datetime"},
		},
	}
	want := []string{"bash", "file_read", "plan_add_tasks", "plan_update_task", "datetime", "plan_get_tasks"}
	if got := stream.executeStageTools(); !reflect.DeepEqual(got, want) {
		t.Fatalf("executeStageTools()=%#v want %#v", got, want)
	}
}

func TestCoderPlanningPromptUsesCoderPromptsConfig(t *testing.T) {
	stream := &coderPlanningStream{
		engine: &LLMAgentEngine{cfg: config.Config{
			CoderPrompts: config.CoderPromptsConfig{
				PlanningPrompt: "custom {{agent_key}} {{workspace_dir}} {{plan_stage_tools}} {{execute_stage_tools}}\nUse {{finalize_planning_tool_name}}.\n{{execute_tool_descriptions}}",
			},
		}},
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
			AgentKey:          "coder",
			AgentName:         "Coder",
			Mode:              "CODER",
			PlanningMode:      true,
			ToolNames:         []string{"bash", "file_read", contracts.FinalizePlanningToolName, "ask_user_question"},
			CoderSystemPrompt: "CODER {{agent_key}} {{agent_name}} {{available_tools}} {{execute_stage_tools}} {{bash_tool_name}}",
		},
		settings: contracts.PlanExecuteSettings{
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

func TestCoderSummaryMessagesReuseExecutePrefix(t *testing.T) {
	executePrefix := []openAIMessage{
		{Role: "system", Content: "execute system prompt"},
		{Role: "user", Content: "execute confirmed plan"},
		{Role: "assistant", Content: "execution complete"},
	}
	stream := &coderPlanningStream{
		engine: &LLMAgentEngine{cfg: config.Config{
			CoderPrompts: config.CoderPromptsConfig{
				SummarySystemPrompt:       "summary system prompt must not be used",
				SummaryUserPromptTemplate: "summary {{original_request}} {{confirmed_plan}}",
			},
		}},
		req:                 api.QueryRequest{Message: "build it"},
		summaryBaseMessages: append([]openAIMessage(nil), executePrefix...),
	}
	got := stream.summaryMessages("confirmed markdown")
	if len(got) != len(executePrefix)+1 {
		t.Fatalf("expected one appended summary message, got %#v", got)
	}
	if !reflect.DeepEqual(got[:len(executePrefix)], executePrefix) {
		t.Fatalf("summary prefix changed: got %#v want %#v", got[:len(executePrefix)], executePrefix)
	}
	last := got[len(got)-1]
	if last.Role != "user" || last.Content != "summary build it confirmed markdown" {
		t.Fatalf("unexpected appended summary user message %#v", last)
	}
	if got[0].Content == "summary system prompt must not be used" {
		t.Fatalf("summary system prompt replaced execute prefix: %#v", got)
	}
}

func TestCoderPlanningConfirmationUsesPlanMode(t *testing.T) {
	stream := &coderPlanningStream{
		session: contracts.QuerySession{RunID: "run_1"},
		execCtx: &contracts.ExecutionContext{
			PlanningRevision: 1,
			PlanningState: &contracts.PlanningRuntimeState{
				PlanningID:   "run_1_planning_1",
				PlanningFile: "/tmp/chat_1/.tools/plans/run_1_planning_1.md",
				ToolCallID:   "tool_plan",
				ToolName:     contracts.FinalizePlanningToolName,
			},
			Budget: contracts.Budget{
				Tool: contracts.RetryPolicy{Timeout: 120},
				Hitl: contracts.HitlPolicy{Timeout: 600},
			},
		},
	}
	ask := stream.planConfirmationAsk()
	if ask.AwaitingID != "tool_plan" || ask.Mode != "plan" || ask.ViewportType != "builtin" || ask.ViewportKey != "plan" {
		t.Fatalf("expected plan confirmation ask, got %#v", ask)
	}
	if ask.Timeout != 0 {
		t.Fatalf("expected planning confirmation to wait forever with timeout 0, got %#v", ask)
	}
	if len(ask.Questions) != 0 || len(ask.Approvals) != 0 || len(ask.Plan) == 0 {
		t.Fatalf("expected one plan and no questions/approvals, got %#v", ask)
	}
	if ask.Plan["id"] != "confirm" || ask.Plan["planningId"] != "run_1_planning_1" ||
		ask.Plan["planningFile"] != "/tmp/chat_1/.tools/plans/run_1_planning_1.md" || ask.Plan["title"] != "实施此计划？" {
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
	if !stream.planDone || stream.completed || stream.summaryDone || !stream.confirmationPending {
		t.Fatalf("unexpected planning stream state: %#v", stream)
	}
	if len(stream.pending) != 1 {
		t.Fatalf("expected one pending confirmation ask, got %#v", stream.pending)
	}
	ask, ok := stream.pending[0].(contracts.DeltaAwaitAsk)
	if !ok || ask.Mode != "plan" || ask.AwaitingID != "tool_plan" || ask.Plan["planningId"] != "run_1_planning_1" {
		t.Fatalf("expected plan confirmation ask, got %#v", stream.pending[0])
	}
}

func TestCoderPlanningStageEOFWithoutPlanButAssistantTextCompletes(t *testing.T) {
	stream := &coderPlanningStream{
		execCtx: &contracts.ExecutionContext{},
		executeMessages: []openAIMessage{
			{Role: "user", Content: "这个怎么产生的"},
			{Role: "assistant", Content: "这是由 planningMode 误触发产生的。"},
		},
	}
	if err := stream.afterStageEOF(); err != nil {
		t.Fatalf("afterStageEOF: %v", err)
	}
	if !stream.planDone || !stream.completed || !stream.summaryDone {
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
		executeMessages: []openAIMessage{
			{Role: "assistant", ToolCalls: []openAIToolCall{{ID: "tool_1"}}},
		},
	}
	if err := stream.afterStageEOF(); err != nil {
		t.Fatalf("afterStageEOF: %v", err)
	}
	if !stream.planDone || !stream.completed || !stream.summaryDone {
		t.Fatalf("expected planning stream to complete after error, got %#v", stream)
	}
	if len(stream.pending) != 1 {
		t.Fatalf("expected one pending error, got %#v", stream.pending)
	}
	errDelta, ok := stream.pending[0].(contracts.DeltaError)
	if !ok {
		t.Fatalf("expected DeltaError, got %#v", stream.pending[0])
	}
	if errDelta.Error["code"] != "plan_not_created" || errDelta.Error["category"] != "model" {
		t.Fatalf("expected model plan_not_created error, got %#v", errDelta.Error)
	}
}

func TestCoderPlanningFeedbackStageEOFWithoutPlanCompletes(t *testing.T) {
	stream := &coderPlanningStream{
		execCtx:               &contracts.ExecutionContext{},
		currentPlanIsFeedback: true,
	}
	if err := stream.afterStageEOF(); err != nil {
		t.Fatalf("afterStageEOF: %v", err)
	}
	if !stream.planDone || !stream.completed || !stream.summaryDone {
		t.Fatalf("expected feedback stage to complete normally, got %#v", stream)
	}
	if len(stream.pending) != 0 {
		t.Fatalf("did not expect pending deltas for empty feedback, got %#v", stream.pending)
	}
}

func TestPlanningStageHasAssistantText(t *testing.T) {
	cases := []struct {
		name     string
		messages []openAIMessage
		want     bool
	}{
		{
			name:     "assistant string",
			messages: []openAIMessage{{Role: "assistant", Content: "请补充一下范围。"}},
			want:     true,
		},
		{
			name: "assistant content parts",
			messages: []openAIMessage{{
				Role: "assistant",
				Content: []any{
					map[string]any{"type": "text", "text": "已取消执行计划。"},
				},
			}},
			want: true,
		},
		{
			name:     "user text only",
			messages: []openAIMessage{{Role: "user", Content: "hello"}},
			want:     false,
		},
		{
			name:     "assistant tool call only",
			messages: []openAIMessage{{Role: "assistant", ToolCalls: []openAIToolCall{{ID: "tool_1"}}}},
			want:     false,
		},
		{
			name: "history assistant before current user",
			messages: []openAIMessage{
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
