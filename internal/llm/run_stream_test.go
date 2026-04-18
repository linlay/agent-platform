package llm

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"agent-platform-runner-go/internal/api"
	contracts "agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/frontendtools"
	"agent-platform-runner-go/internal/hitl"
)

type stubToolExecutor struct {
	defs []api.ToolDetailResponse
}

func (s stubToolExecutor) Definitions() []api.ToolDetailResponse { return s.defs }

func (s stubToolExecutor) Invoke(_ context.Context, _ string, _ map[string]any, _ *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
	return contracts.ToolExecutionResult{}, nil
}

type recordedToolInvocation struct {
	name string
	args map[string]any
}

type recordingToolExecutor struct {
	defs        []api.ToolDetailResponse
	result      contracts.ToolExecutionResult
	invocations []recordedToolInvocation
}

func (r *recordingToolExecutor) Definitions() []api.ToolDetailResponse { return r.defs }

func (r *recordingToolExecutor) Invoke(_ context.Context, name string, args map[string]any, _ *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
	cloned := make(map[string]any, len(args))
	for key, value := range args {
		cloned[key] = value
	}
	r.invocations = append(r.invocations, recordedToolInvocation{name: name, args: cloned})
	if r.result.Output == "" && r.result.Structured == nil && r.result.Error == "" && r.result.ExitCode == 0 {
		return contracts.ToolExecutionResult{Output: "ok", ExitCode: 0}, nil
	}
	return r.result, nil
}

type stubChecker struct {
	result hitl.InterceptResult
	tools  map[string]api.ToolDetailResponse
}

func (s stubChecker) Check(string, int) hitl.InterceptResult { return s.result }

func (s stubChecker) Tool(name string) (api.ToolDetailResponse, bool) {
	tool, ok := s.tools[strings.ToLower(strings.TrimSpace(name))]
	return tool, ok
}

func (s stubChecker) Tools() []api.ToolDetailResponse {
	items := make([]api.ToolDetailResponse, 0, len(s.tools))
	for _, tool := range s.tools {
		items = append(items, tool)
	}
	return items
}

func approvalToolDefinition() api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Name: "_ask_user_approval_",
		Meta: map[string]any{
			"kind":          "frontend",
			"sourceType":    "local",
			"viewportType":  "builtin",
			"viewportKey":   "confirm_dialog",
			"clientVisible": true,
		},
	}
}

func TestPreToolInvocationDeltas_QuestionUsesFrontendHandlerPayload(t *testing.T) {
	tool := api.ToolDetailResponse{
		Name: "_ask_user_question_",
		Meta: map[string]any{
			"kind":          "frontend",
			"sourceType":    "local",
			"viewportType":  "builtin",
			"viewportKey":   "confirm_dialog",
			"clientVisible": true,
		},
	}
	runControl := contracts.NewRunControl(context.Background(), "run_1")
	stream := &llmRunStream{
		engine: &LLMAgentEngine{
			tools:    stubToolExecutor{defs: []api.ToolDetailResponse{tool}},
			frontend: frontendtools.NewDefaultRegistry(),
		},
		session:    contracts.QuerySession{RunID: "run_1"},
		runControl: runControl,
		execCtx: &contracts.ExecutionContext{
			Budget: contracts.Budget{Tool: contracts.RetryPolicy{TimeoutMs: 50}},
		},
	}

	deltas := stream.preToolInvocationDeltas("tool_1", "_ask_user_question_", map[string]any{
		"mode": "question",
		"questions": []any{
			map[string]any{
				"question":            "How many people?",
				"type":                "number",
				"placeholder":         "3",
				"allowFreeText":       false,
				"freeTextPlaceholder": "removed",
				"multiSelect":         false,
				"options":             []any{map[string]any{"label": "unused"}},
			},
		},
	})
	if len(deltas) != 1 {
		t.Fatalf("expected 1 delta, got %#v", deltas)
	}
	payload, ok := deltas[0].(contracts.DeltaAwaitPayload)
	if !ok {
		t.Fatalf("expected DeltaAwaitPayload, got %#v", deltas[0])
	}
	if payload.AwaitingID != "tool_1" || len(payload.Questions) != 1 {
		t.Fatalf("unexpected payload %#v", payload)
	}
	question := payload.Questions[0].(map[string]any)
	if _, ok := question["allowFreeText"]; ok {
		t.Fatalf("expected non-select question to be sanitized, got %#v", question)
	}
}

func TestPrepareToolCall_InvalidAskUserQuestionArgsReturnToolError(t *testing.T) {
	tool := api.ToolDetailResponse{
		Name: "_ask_user_question_",
		Meta: map[string]any{
			"kind":          "frontend",
			"sourceType":    "local",
			"viewportType":  "builtin",
			"viewportKey":   "confirm_dialog",
			"clientVisible": true,
		},
	}
	stream := &llmRunStream{
		engine: &LLMAgentEngine{
			tools:    stubToolExecutor{defs: []api.ToolDetailResponse{tool}},
			frontend: frontendtools.NewDefaultRegistry(),
		},
		session: contracts.QuerySession{RunID: "run_1"},
		execCtx: &contracts.ExecutionContext{},
	}

	invocation, deltas, toolMsg := stream.prepareToolCall(openAIToolCall{
		ID:   "tool_1",
		Type: "function",
		Function: openAIFunctionCall{
			Name:      "_ask_user_question_",
			Arguments: `{"mode":"question","questions":[{"question":"Pick a plan","type":"select"}]}`,
		},
	})
	if invocation != nil {
		t.Fatalf("expected no invocation, got %#v", invocation)
	}
	if len(deltas) != 1 {
		t.Fatalf("expected one error delta, got %#v", deltas)
	}
	result, ok := deltas[0].(contracts.DeltaToolResult)
	if !ok {
		t.Fatalf("expected DeltaToolResult, got %#v", deltas[0])
	}
	if result.Result.Error != "invalid_tool_arguments" || !strings.Contains(result.Result.Output, "options is required for select questions") {
		t.Fatalf("unexpected tool result %#v", result)
	}
	toolContent, _ := toolMsg.Content.(string)
	if toolMsg == nil || !strings.Contains(toolContent, "options is required for select questions") {
		t.Fatalf("unexpected tool message %#v", toolMsg)
	}
}

func TestPreToolInvocationDeltas_ApprovalUsesFrontendHandlerAwaitAsk(t *testing.T) {
	tool := approvalToolDefinition()
	stream := &llmRunStream{
		engine: &LLMAgentEngine{
			tools:    stubToolExecutor{defs: []api.ToolDetailResponse{tool}},
			frontend: frontendtools.NewDefaultRegistry(),
		},
		session: contracts.QuerySession{RunID: "run_1"},
		execCtx: &contracts.ExecutionContext{
			Budget: contracts.Budget{Tool: contracts.RetryPolicy{TimeoutMs: 50}},
		},
		runControl: contracts.NewRunControl(context.Background(), "run_1"),
	}

	deltas := stream.preToolInvocationDeltas("tool_1", "_ask_user_approval_", map[string]any{
		"mode": "approval",
		"questions": []any{
			map[string]any{
				"question": "Need confirmation",
				"options": []any{
					map[string]any{"label": "Approve", "value": "approve"},
				},
			},
		},
	})
	if len(deltas) != 1 {
		t.Fatalf("expected 1 delta, got %#v", deltas)
	}
	awaitAsk, ok := deltas[0].(contracts.DeltaAwaitAsk)
	if !ok {
		t.Fatalf("expected DeltaAwaitAsk, got %#v", deltas[0])
	}
	if awaitAsk.Mode != "approval" || awaitAsk.AwaitingID != "tool_1" {
		t.Fatalf("unexpected await ask %#v", awaitAsk)
	}
	expectedQuestions := []any{
		map[string]any{
			"question": "Need confirmation",
			"options": []any{
				map[string]any{"label": "Approve", "value": "approve"},
			},
		},
	}
	if !reflect.DeepEqual(awaitAsk.Questions, expectedQuestions) {
		t.Fatalf("unexpected approval questions %#v", awaitAsk.Questions)
	}
}

func TestBashHITLApprovalUsesAwaitingForAllViewports(t *testing.T) {
	tests := []struct {
		name                 string
		rule                 hitl.FlatRule
		initialCommand       string
		parsedCommand        hitl.CommandComponents
		submitParams         any
		expectedCommand      string
		expectedView         string
		expectedKey          string
		expectedAwaitPayload map[string]any
		expectedAnswerAction string
	}{
		{
			name: "builtin confirm dialog",
			rule: hitl.FlatRule{
				Match:        "push",
				Level:        1,
				ViewportType: "builtin",
				ViewportKey:  "confirm_dialog",
			},
			initialCommand: "git push origin main",
			parsedCommand: hitl.CommandComponents{
				BaseCommand: "git",
				Tokens:      []string{"push", "origin", "main"},
			},
			submitParams: []any{
				map[string]any{
					"question": "git push origin main",
					"answer":   "Approve",
					"value":    "approve",
				},
			},
			expectedCommand: "git push origin main",
			expectedView:    "builtin",
			expectedKey:     "confirm_dialog",
		},
		{
			name: "leave html viewport override",
			rule: hitl.FlatRule{
				Match:        "create-leave",
				Level:        1,
				ViewportType: "html",
				ViewportKey:  "leave_form",
			},
			initialCommand: `mock create-leave --payload '{"employee_id":"E1001","days":3}'`,
			parsedCommand: hitl.CommandComponents{
				BaseCommand: "mock",
				Tokens:      []string{"create-leave", "--payload", `{"employee_id":"E1001","days":3}`},
			},
			submitParams: map[string]any{
				"action": "submit",
				"payload": map[string]any{
					"employee_id": "E1001",
					"days":        2,
				},
			},
			expectedCommand:      `mock create-leave --payload '{"days":2,"employee_id":"E1001"}'`,
			expectedView:         "html",
			expectedKey:          "leave_form",
			expectedAwaitPayload: map[string]any{"employee_id": "E1001", "days": float64(3)},
			expectedAnswerAction: "submit",
		},
		{
			name: "expense html viewport override",
			rule: hitl.FlatRule{
				Match:        "create-expense",
				Level:        1,
				ViewportType: "html",
				ViewportKey:  "expense_form",
			},
			initialCommand: `mock create-expense --payload '{"employee_id":"E1001","total_amount":1280.5}'`,
			parsedCommand: hitl.CommandComponents{
				BaseCommand: "mock",
				Tokens:      []string{"create-expense", "--payload", `{"employee_id":"E1001","total_amount":1280.5}`},
			},
			submitParams: map[string]any{
				"action": "submit",
				"payload": map[string]any{
					"employee_id":  "E1001",
					"total_amount": 640.25,
				},
			},
			expectedCommand:      `mock create-expense --payload '{"employee_id":"E1001","total_amount":640.25}'`,
			expectedView:         "html",
			expectedKey:          "expense_form",
			expectedAwaitPayload: map[string]any{"employee_id": "E1001", "total_amount": 1280.5},
			expectedAnswerAction: "submit",
		},
		{
			name: "procurement html viewport override",
			rule: hitl.FlatRule{
				Match:        "create-procurement",
				Level:        1,
				ViewportType: "html",
				ViewportKey:  "procurement_form",
			},
			initialCommand: `mock create-procurement --payload '{"delivery_city":"Shanghai","requester_id":"E1001"}'`,
			parsedCommand: hitl.CommandComponents{
				BaseCommand: "mock",
				Tokens:      []string{"create-procurement", "--payload", `{"delivery_city":"Shanghai","requester_id":"E1001"}`},
			},
			submitParams: map[string]any{
				"action": "submit",
				"payload": map[string]any{
					"delivery_city": "Hangzhou",
					"requester_id":  "E1001",
				},
			},
			expectedCommand:      `mock create-procurement --payload '{"delivery_city":"Hangzhou","requester_id":"E1001"}'`,
			expectedView:         "html",
			expectedKey:          "procurement_form",
			expectedAwaitPayload: map[string]any{"delivery_city": "Shanghai", "requester_id": "E1001"},
			expectedAnswerAction: "submit",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			executor := &recordingToolExecutor{
				defs: []api.ToolDetailResponse{approvalToolDefinition()},
				result: contracts.ToolExecutionResult{
					Output:   "executed",
					ExitCode: 0,
				},
			}
			runControl := contracts.NewRunControl(context.Background(), "run_1")
			stream := &llmRunStream{
				ctx: context.Background(),
				engine: &LLMAgentEngine{
					tools:    executor,
					frontend: frontendtools.NewDefaultRegistry(),
				},
				session: contracts.QuerySession{
					RequestID: "req_1",
					ChatID:    "chat_1",
					RunID:     "run_1",
				},
				runControl: runControl,
				execCtx: &contracts.ExecutionContext{
					Budget: contracts.Budget{Tool: contracts.RetryPolicy{TimeoutMs: 50}},
				},
			}
			invocation := &preparedToolInvocation{
				toolID:   "tool_1",
				toolName: "_sandbox_bash_",
				args: map[string]any{
					"command": tc.initialCommand,
				},
			}
			result := hitl.InterceptResult{
				Intercepted:   true,
				Rule:          tc.rule,
				ParsedCommand: tc.parsedCommand,
			}

			if err := stream.emitHITLConfirmDeltas(invocation, result); err != nil {
				t.Fatalf("emitHITLConfirmDeltas returned error: %v", err)
			}
			if stream.hitlAwaitingID != buildHITLAwaitingID("tool_1") {
				t.Fatalf("expected bash HITL awaiting id, got %q", stream.hitlAwaitingID)
			}

			var awaitAsk contracts.DeltaAwaitAsk
			foundAwaitAsk := false
			for _, delta := range stream.pending {
				if toolCall, ok := delta.(contracts.DeltaToolCall); ok {
					t.Fatalf("did not expect synthetic approval tool call, got %#v", toolCall)
				}
				if ask, ok := delta.(contracts.DeltaAwaitAsk); ok {
					awaitAsk = ask
					foundAwaitAsk = true
				}
			}
			if !foundAwaitAsk {
				t.Fatalf("expected awaiting.ask delta, got %#v", stream.pending)
			}
			if awaitAsk.Mode != "approval" || awaitAsk.ViewportType != tc.expectedView || awaitAsk.ViewportKey != tc.expectedKey {
				t.Fatalf("unexpected await ask %#v", awaitAsk)
			}
			if tc.expectedAwaitPayload != nil {
				if !reflect.DeepEqual(awaitAsk.Payload, tc.expectedAwaitPayload) {
					t.Fatalf("expected approval form payload %#v, got %#v", tc.expectedAwaitPayload, awaitAsk)
				}
				if len(awaitAsk.Questions) != 0 {
					t.Fatalf("expected form approval to omit questions, got %#v", awaitAsk.Questions)
				}
			} else {
				questions := awaitAsk.Questions
				if len(questions) != 1 {
					t.Fatalf("expected one approval question, got %#v", awaitAsk.Questions)
				}
				firstQuestion, ok := questions[0].(map[string]any)
				if !ok {
					t.Fatalf("expected approval question object, got %#v", questions[0])
				}
				if firstQuestion["question"] != tc.submitParams.([]any)[0].(map[string]any)["question"] {
					t.Fatalf("expected approval question to use original command, got %#v", firstQuestion)
				}
			}

			ack := runControl.ResolveSubmit(api.SubmitRequest{
				RunID:      "run_1",
				AwaitingID: stream.hitlAwaitingID,
				Params:     tc.submitParams,
			})
			if !ack.Accepted {
				t.Fatalf("expected submit to be accepted, got %#v", ack)
			}
			if err := stream.awaitHITLSubmitAndExecute(); err != nil {
				t.Fatalf("awaitHITLSubmitAndExecute returned error: %v", err)
			}
			if len(executor.invocations) != 1 {
				t.Fatalf("expected original bash tool to run once, got %#v", executor.invocations)
			}
			if executor.invocations[0].name != "_sandbox_bash_" {
				t.Fatalf("expected original bash tool to execute, got %#v", executor.invocations[0])
			}
			if got := executor.invocations[0].args["command"]; got != tc.expectedCommand {
				t.Fatalf("expected command %q, got %#v", tc.expectedCommand, got)
			}

			foundRequestSubmit := false
			foundAwaitingAnswer := false
			foundOriginalResult := false
			for _, delta := range stream.pending {
				switch typed := delta.(type) {
				case contracts.DeltaRequestSubmit:
					if typed.AwaitingID == buildHITLAwaitingID("tool_1") {
						foundRequestSubmit = true
					}
				case contracts.DeltaAwaitingAnswer:
					if typed.AwaitingID == buildHITLAwaitingID("tool_1") {
						foundAwaitingAnswer = true
						if tc.expectedAnswerAction != "" && typed.Answer["action"] != tc.expectedAnswerAction {
							t.Fatalf("expected awaiting.answer action %q, got %#v", tc.expectedAnswerAction, typed.Answer)
						}
					}
				case contracts.DeltaToolResult:
					if typed.ToolName == "_sandbox_bash_" {
						foundOriginalResult = true
					}
				}
			}
			if !foundRequestSubmit || !foundAwaitingAnswer || !foundOriginalResult {
				t.Fatalf("expected submit/answer/results deltas, got %#v", stream.pending)
			}
		})
	}
}

func TestAwaitHITLSubmitAndExecute_RejectEmitsCancelledAnswer(t *testing.T) {
	runControl := contracts.NewRunControl(context.Background(), "run_1")
	stream := &llmRunStream{
		ctx: context.Background(),
		engine: &LLMAgentEngine{
			tools:    &recordingToolExecutor{defs: []api.ToolDetailResponse{approvalToolDefinition()}},
			frontend: frontendtools.NewDefaultRegistry(),
		},
		session: contracts.QuerySession{
			RequestID: "req_1",
			ChatID:    "chat_1",
			RunID:     "run_1",
		},
		runControl: runControl,
		execCtx: &contracts.ExecutionContext{
			Budget: contracts.Budget{Tool: contracts.RetryPolicy{TimeoutMs: 50}},
		},
		hitlPendingCall: &preparedToolInvocation{
			toolID:   "tool_1",
			toolName: "_sandbox_bash_",
			args: map[string]any{
				"command": "docker rmi nginx:latest",
			},
		},
		hitlMatch: &hitl.InterceptResult{
			Intercepted: true,
			Rule: hitl.FlatRule{
				Match:        "rmi",
				Level:        1,
				ViewportType: "builtin",
				ViewportKey:  "confirm_dialog",
			},
		},
		hitlAwaitingID: buildHITLAwaitingID("tool_1"),
		hitlAwaitArgs: map[string]any{
			"mode": "approval",
			"questions": []any{
				map[string]any{
					"question": "docker rmi nginx:latest",
					"header":   "Bash Approval",
					"options": []any{
						map[string]any{"label": "Approve", "value": "approve"},
						map[string]any{"label": "Reject", "value": "reject"},
					},
				},
			},
		},
	}
	runControl.ExpectSubmit(stream.hitlAwaitingID)
	ack := runControl.ResolveSubmit(api.SubmitRequest{
		RunID:      "run_1",
		AwaitingID: stream.hitlAwaitingID,
		Params: []any{
			map[string]any{
				"question": "docker rmi nginx:latest",
				"answer":   "Reject",
				"value":    "reject",
			},
		},
	})
	if !ack.Accepted {
		t.Fatalf("expected submit to be accepted, got %#v", ack)
	}

	if err := stream.awaitHITLSubmitAndExecute(); err != nil {
		t.Fatalf("awaitHITLSubmitAndExecute returned error: %v", err)
	}

	foundAnswer := false
	foundResult := false
	for _, delta := range stream.pending {
		switch typed := delta.(type) {
		case contracts.DeltaAwaitingAnswer:
			if typed.AwaitingID == buildHITLAwaitingID("tool_1") {
				foundAnswer = true
				if typed.Answer["mode"] != "approval" {
					t.Fatalf("unexpected reject answer %#v", typed.Answer)
				}
				questions, ok := typed.Answer["questions"].([]map[string]any)
				if !ok || len(questions) != 1 || questions[0]["value"] != "reject" {
					t.Fatalf("unexpected reject questions %#v", typed.Answer)
				}
			}
		case contracts.DeltaToolResult:
			if typed.ToolName == "_sandbox_bash_" {
				foundResult = true
				if typed.Result.Error != "user_rejected" {
					t.Fatalf("expected user_rejected tool result, got %#v", typed.Result)
				}
			}
		}
	}
	if !foundAnswer || !foundResult {
		t.Fatalf("expected reject answer and tool result, got %#v", stream.pending)
	}
}

func TestAwaitHITLSubmitAndExecute_TimeoutEmitsTerminalAnswer(t *testing.T) {
	stream := &llmRunStream{
		ctx: context.Background(),
		engine: &LLMAgentEngine{
			tools:    &recordingToolExecutor{defs: []api.ToolDetailResponse{approvalToolDefinition()}},
			frontend: frontendtools.NewDefaultRegistry(),
		},
		session: contracts.QuerySession{
			RequestID: "req_1",
			ChatID:    "chat_1",
			RunID:     "run_1",
		},
		runControl: contracts.NewRunControl(context.Background(), "run_1"),
		execCtx: &contracts.ExecutionContext{
			Budget: contracts.Budget{Tool: contracts.RetryPolicy{TimeoutMs: 1}},
		},
		hitlPendingCall: &preparedToolInvocation{
			toolID:   "tool_1",
			toolName: "_sandbox_bash_",
			args: map[string]any{
				"command": "docker rmi nginx:latest",
			},
		},
		hitlMatch: &hitl.InterceptResult{
			Intercepted: true,
			Rule: hitl.FlatRule{
				Match:        "rmi",
				Level:        1,
				ViewportType: "builtin",
				ViewportKey:  "confirm_dialog",
			},
		},
		hitlAwaitingID: buildHITLAwaitingID("tool_1"),
		hitlAwaitArgs: map[string]any{
			"mode": "approval",
		},
	}
	stream.runControl.ExpectSubmit(stream.hitlAwaitingID)

	if err := stream.awaitHITLSubmitAndExecute(); err != nil {
		t.Fatalf("awaitHITLSubmitAndExecute returned error: %v", err)
	}

	foundAnswer := false
	foundResult := false
	for _, delta := range stream.pending {
		switch typed := delta.(type) {
		case contracts.DeltaAwaitingAnswer:
			if typed.AwaitingID == buildHITLAwaitingID("tool_1") {
				foundAnswer = true
				if typed.Answer["reason"] != "timeout" || typed.Answer["code"] != "hitl_timeout" {
					t.Fatalf("unexpected timeout answer %#v", typed.Answer)
				}
			}
		case contracts.DeltaToolResult:
			if typed.ToolName == "_sandbox_bash_" {
				foundResult = true
				if typed.Result.Error != "hitl_timeout" {
					t.Fatalf("expected hitl_timeout tool result, got %#v", typed.Result)
				}
			}
		}
	}
	if !foundAnswer || !foundResult {
		t.Fatalf("expected timeout answer and tool result, got %#v", stream.pending)
	}
}

func TestInvokeActiveToolCallUsesSkillScopedChecker(t *testing.T) {
	executor := &recordingToolExecutor{
		defs: []api.ToolDetailResponse{approvalToolDefinition()},
		result: contracts.ToolExecutionResult{
			Output:   "executed",
			ExitCode: 0,
		},
	}
	runControl := contracts.NewRunControl(context.Background(), "run_1")
	stream := &llmRunStream{
		ctx: context.Background(),
		engine: &LLMAgentEngine{
			tools:    executor,
			frontend: frontendtools.NewDefaultRegistry(),
		},
		checker: stubChecker{
			result: hitl.InterceptResult{
				Intercepted: true,
				Rule: hitl.FlatRule{
					Match:        "push",
					Level:        1,
					ViewportType: "builtin",
					ViewportKey:  "confirm_dialog",
				},
				ParsedCommand: hitl.CommandComponents{
					BaseCommand: "git",
					Tokens:      []string{"push", "origin", "main"},
				},
			},
		},
		session: contracts.QuerySession{
			RequestID: "req_1",
			ChatID:    "chat_1",
			RunID:     "run_1",
		},
		runControl: runControl,
		execCtx: &contracts.ExecutionContext{
			Budget: contracts.Budget{Tool: contracts.RetryPolicy{TimeoutMs: 50}},
		},
		activeToolCall: &preparedToolInvocation{
			toolID:   "tool_1",
			toolName: "_sandbox_bash_",
			args: map[string]any{
				"command": "git push origin main",
			},
		},
	}

	if err := stream.invokeActiveToolCall(); err != nil {
		t.Fatalf("invokeActiveToolCall returned error: %v", err)
	}
	if stream.hitlAwaitingID != buildHITLAwaitingID("tool_1") {
		t.Fatalf("expected awaiting id, got %q", stream.hitlAwaitingID)
	}
	if len(executor.invocations) != 0 {
		t.Fatalf("expected bash execution to wait for approval, got %#v", executor.invocations)
	}
}

func TestExtractCommandPayload(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		expected map[string]any
	}{
		{
			name:     "mock command payload",
			command:  `mock create-leave --payload '{"employee_id":"E1001","days":3}'`,
			expected: map[string]any{"employee_id": "E1001", "days": float64(3)},
		},
		{
			name:     "non mock command payload",
			command:  `demo submit-request --payload '{"request_id":"REQ-1","priority":"high"}'`,
			expected: map[string]any{"request_id": "REQ-1", "priority": "high"},
		},
		{
			name:    "payload file is ignored",
			command: `mock create-leave --payload-file ./leave.json`,
		},
		{
			name:    "missing payload value",
			command: `mock create-leave --payload`,
		},
		{
			name:    "invalid json payload",
			command: `mock create-leave --payload '{invalid-json}'`,
		},
		{
			name:    "payload must be object",
			command: `mock create-leave --payload '["E1001"]'`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload := extractCommandPayload(hitl.ParseCommandComponents(tc.command))
			if !reflect.DeepEqual(payload, tc.expected) {
				t.Fatalf("expected payload %#v, got %#v", tc.expected, payload)
			}
		})
	}
}

func TestBuildFormApprovalArgsFallsBackToOriginalCommandPayload(t *testing.T) {
	stream := &llmRunStream{
		session: contracts.QuerySession{RunID: "run_1"},
	}
	args := stream.buildFormApprovalArgs(hitl.InterceptResult{
		Rule: hitl.FlatRule{
			ViewportType: "html",
			ViewportKey:  "leave_form",
		},
		ParsedCommand: hitl.CommandComponents{
			BaseCommand: "mock",
			Tokens:      []string{"create-leave", "--payload", "{employee_id:E1001}"},
		},
		OriginalCommand: `mock create-leave --payload {"employee_id":"E1001","days":3}`,
	})
	payload, ok := args["payload"].(map[string]any)
	if !ok {
		t.Fatalf("expected payload in form approval args, got %#v", args)
	}
	expected := map[string]any{"employee_id": "E1001", "days": float64(3)}
	if !reflect.DeepEqual(payload, expected) {
		t.Fatalf("expected payload %#v, got %#v", expected, payload)
	}
}

func TestReconstructCommandWithPayload(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		payload  map[string]any
		expected string
	}{
		{
			name:     "leave",
			command:  `mock create-leave --payload '{"employee_id":"E1001","days":3}'`,
			payload:  map[string]any{"employee_id": "E1001", "days": 2},
			expected: `mock create-leave --payload '{"days":2,"employee_id":"E1001"}'`,
		},
		{
			name:     "expense",
			command:  `mock create-expense --payload '{"employee_id":"E1001","total_amount":1280.5}'`,
			payload:  map[string]any{"employee_id": "E1001", "total_amount": 1280.5},
			expected: `mock create-expense --payload '{"employee_id":"E1001","total_amount":1280.5}'`,
		},
		{
			name:     "procurement",
			command:  `mock create-procurement --payload '{"requester_id":"E1001","delivery_city":"Shanghai"}'`,
			payload:  map[string]any{"requester_id": "E1001", "delivery_city": "Shanghai"},
			expected: `mock create-procurement --payload '{"delivery_city":"Shanghai","requester_id":"E1001"}'`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rebuilt, err := reconstructCommandWithPayload(tc.command, tc.payload)
			if err != nil {
				t.Fatalf("reconstructCommandWithPayload returned error: %v", err)
			}
			if rebuilt != tc.expected {
				t.Fatalf("expected %q, got %q", tc.expected, rebuilt)
			}
		})
	}

	if _, err := reconstructCommandWithPayload(`mock create-leave --payload-file ./leave.json`, map[string]any{"employee_id": "E1001"}); err == nil {
		t.Fatal("expected command without --payload to fail reconstruction")
	}

	withQuote, err := reconstructCommandWithPayload(`mock create-leave --payload '{"reason":"o'\''hara"}'`, map[string]any{"reason": "o'hara"})
	if err != nil {
		t.Fatalf("reconstructCommandWithPayload returned error for apostrophe payload: %v", err)
	}
	if withQuote != `mock create-leave --payload '{"reason":"o'"'"'hara"}'` {
		t.Fatalf("expected shell-safe quoted payload, got %q", withQuote)
	}
}

func TestInvokeActiveToolCallAutoApprovesBuiltinLevelInCurrentRun(t *testing.T) {
	executor := &recordingToolExecutor{
		defs: []api.ToolDetailResponse{approvalToolDefinition()},
		result: contracts.ToolExecutionResult{
			Output:   "executed",
			ExitCode: 0,
		},
	}
	stream := &llmRunStream{
		ctx: context.Background(),
		engine: &LLMAgentEngine{
			tools:    executor,
			frontend: frontendtools.NewDefaultRegistry(),
		},
		checker: stubChecker{
			result: hitl.InterceptResult{
				Intercepted: true,
				Rule: hitl.FlatRule{
					Match:        "push",
					Level:        2,
					ViewportType: "builtin",
					ViewportKey:  "confirm_dialog",
				},
			},
		},
		session: contracts.QuerySession{
			RequestID: "req_1",
			ChatID:    "chat_1",
			RunID:     "run_1",
		},
		execCtx: &contracts.ExecutionContext{
			Budget:            contracts.Budget{Tool: contracts.RetryPolicy{TimeoutMs: 50}},
			AutoApproveLevels: map[int]bool{2: true},
		},
		activeToolCall: &preparedToolInvocation{
			toolID:   "tool_1",
			toolName: "_sandbox_bash_",
			args: map[string]any{
				"command": "git push origin main",
			},
		},
	}

	if err := stream.invokeActiveToolCall(); err != nil {
		t.Fatalf("invokeActiveToolCall returned error: %v", err)
	}
	if len(executor.invocations) != 1 {
		t.Fatalf("expected auto-approved command to execute once, got %#v", executor.invocations)
	}
	for _, delta := range stream.pending {
		if _, ok := delta.(contracts.DeltaAwaitAsk); ok {
			t.Fatalf("did not expect approval prompt when auto-approving, got %#v", stream.pending)
		}
	}
}

func TestInvokeActiveToolCallDoesNotAutoApproveHTMLViewport(t *testing.T) {
	executor := &recordingToolExecutor{
		defs: []api.ToolDetailResponse{approvalToolDefinition()},
		result: contracts.ToolExecutionResult{
			Output:   "executed",
			ExitCode: 0,
		},
	}
	stream := &llmRunStream{
		ctx: context.Background(),
		engine: &LLMAgentEngine{
			tools:    executor,
			frontend: frontendtools.NewDefaultRegistry(),
		},
		checker: stubChecker{
			result: hitl.InterceptResult{
				Intercepted: true,
				Rule: hitl.FlatRule{
					Match:        "create-leave",
					Level:        2,
					ViewportType: "html",
					ViewportKey:  "leave_form",
				},
				ParsedCommand: hitl.CommandComponents{
					BaseCommand: "mock",
					Tokens:      []string{"create-leave", "--payload", `{"employee_id":"E1001"}`},
				},
			},
		},
		session: contracts.QuerySession{
			RequestID: "req_1",
			ChatID:    "chat_1",
			RunID:     "run_1",
		},
		runControl: contracts.NewRunControl(context.Background(), "run_1"),
		execCtx: &contracts.ExecutionContext{
			Budget:            contracts.Budget{Tool: contracts.RetryPolicy{TimeoutMs: 50}},
			AutoApproveLevels: map[int]bool{2: true},
		},
		activeToolCall: &preparedToolInvocation{
			toolID:   "tool_1",
			toolName: "_sandbox_bash_",
			args: map[string]any{
				"command": `mock create-leave --payload '{"employee_id":"E1001"}'`,
			},
		},
	}

	if err := stream.invokeActiveToolCall(); err != nil {
		t.Fatalf("invokeActiveToolCall returned error: %v", err)
	}
	if len(executor.invocations) != 0 {
		t.Fatalf("expected html command to remain gated by form approval, got %#v", executor.invocations)
	}
	foundAwaitAsk := false
	for _, delta := range stream.pending {
		if _, ok := delta.(contracts.DeltaAwaitAsk); ok {
			foundAwaitAsk = true
		}
	}
	if !foundAwaitAsk {
		t.Fatalf("expected html viewport to keep approval prompt, got %#v", stream.pending)
	}
}
