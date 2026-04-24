package llm

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/chat"
	contracts "agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/frontendtools"
	"agent-platform-runner-go/internal/hitl"
)

func encodedSubmitParams(t *testing.T, value any) api.SubmitParams {
	t.Helper()
	params, err := api.EncodeSubmitParams(value)
	if err != nil {
		t.Fatalf("encode submit params: %v", err)
	}
	return params
}

func sampleLeavePayload(days float64) map[string]any {
	return map[string]any{
		"applicant_id":  "E1001",
		"department_id": "engineering",
		"leave_type":    "annual",
		"start_date":    "2026-04-20",
		"end_date":      "2026-04-22",
		"days":          days,
		"reason":        "family_trip",
	}
}

func sampleLeaveCommand(days float64) string {
	if days == 2 {
		return `mock create-leave --payload '{"applicant_id":"E1001","days":2,"department_id":"engineering","end_date":"2026-04-22","leave_type":"annual","reason":"family_trip","start_date":"2026-04-20"}'`
	}
	return `mock create-leave --payload '{"applicant_id":"E1001","days":3,"department_id":"engineering","end_date":"2026-04-22","leave_type":"annual","reason":"family_trip","start_date":"2026-04-20"}'`
}

func sampleExpenseCommand(amount float64) string {
	return fmt.Sprintf(`mock expense add --payload '{"employee":{"id":"E1001","name":"张三"},"department":{"code":"engineering","name":"工程部"},"expense_type":"travel","currency":"CNY","items":[{"amount":%v,"category":"transport","description":"flight","invoice_id":"INV-001","occurred_on":"2026-04-10"}],"submitted_at":"2026-04-14T10:30:00+08:00","total_amount":%v}'`, amount, amount)
}

func canonicalExpenseCommand(amount float64) string {
	return fmt.Sprintf(`mock expense add --payload '{"currency":"CNY","department":{"code":"engineering","name":"工程部"},"employee":{"id":"E1001","name":"张三"},"expense_type":"travel","items":[{"amount":%v,"category":"transport","description":"flight","invoice_id":"INV-001","occurred_on":"2026-04-10"}],"submitted_at":"2026-04-14T10:30:00+08:00","total_amount":%v}'`, amount, amount)
}

func sampleProcurementCommand(city string) string {
	if city == "Hangzhou" {
		return `mock procurement create --payload '{"delivery_city":"Hangzhou","requester_id":"E1001"}'`
	}
	return `mock procurement create --payload '{"delivery_city":"Shanghai","requester_id":"E1001"}'`
}

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

type commandResultChecker struct {
	results map[string]hitl.InterceptResult
	tools   map[string]api.ToolDetailResponse
}

func (c commandResultChecker) Check(command string, _ int) hitl.InterceptResult {
	return c.results[command]
}

func (c commandResultChecker) Tool(name string) (api.ToolDetailResponse, bool) {
	tool, ok := c.tools[strings.ToLower(strings.TrimSpace(name))]
	return tool, ok
}

func (c commandResultChecker) Tools() []api.ToolDetailResponse {
	items := make([]api.ToolDetailResponse, 0, len(c.tools))
	for _, tool := range c.tools {
		items = append(items, tool)
	}
	return items
}

func bashToolDefinition() api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Name: "_bash_",
		Meta: map[string]any{
			"kind":          "backend",
			"sourceType":    "local",
			"viewportType":  "builtin",
			"viewportKey":   "confirm_dialog",
			"clientVisible": true,
		},
	}
}

func backendToolDefinition(name string) api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Name: name,
		Meta: map[string]any{
			"kind":          "backend",
			"sourceType":    "local",
			"clientVisible": true,
		},
	}
}

func invokeAgentsToolDefinition() api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Name: contracts.InvokeAgentsToolName,
		Meta: map[string]any{
			"kind":          "backend",
			"sourceType":    "local",
			"clientVisible": true,
		},
	}
}

func TestPreToolInvocationDeltas_QuestionRegistersAwaitingContext(t *testing.T) {
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
				"options":             []any{map[string]any{"label": "unused"}},
			},
		},
	})
	if len(deltas) != 0 {
		t.Fatalf("expected no prelude deltas, got %#v", deltas)
	}
	awaiting, ok := runControl.LookupAwaiting("tool_1")
	if !ok {
		t.Fatal("expected awaiting context to be registered")
	}
	if awaiting.Mode != "question" {
		t.Fatalf("unexpected awaiting context %#v", awaiting)
	}
	if awaiting.ItemCount != 1 {
		t.Fatalf("expected question item count 1, got %#v", awaiting)
	}
}

func TestPrepareToolCall_LegacyMultipleReturnsToolError(t *testing.T) {
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
			Arguments: `{"mode":"question","questions":[{"question":"Notification topics","type":"select","multiple":true,"options":[{"label":"产品更新"}]}]}`,
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
	if result.Result.Error != "invalid_tool_arguments" || !strings.Contains(result.Result.Output, "multiple is no longer supported; use type=multi-select") {
		t.Fatalf("unexpected tool result %#v", result)
	}
	toolContent, _ := toolMsg.Content.(string)
	if toolMsg == nil || !strings.Contains(toolContent, "multiple is no longer supported; use type=multi-select") {
		t.Fatalf("unexpected tool message %#v", toolMsg)
	}
}

func TestAppendPublishedArtifactDeltaBatchesMultipleArtifacts(t *testing.T) {
	var pending []contracts.AgentDelta

	appendPublishedArtifactDelta(&pending, contracts.QuerySession{
		ChatID: "chat_1",
		RunID:  "run_1",
	}, []map[string]any{
		{"artifactId": "artifact_1", "name": "report.md"},
		{"artifactId": "artifact_2", "name": "summary.txt"},
	})

	if len(pending) != 1 {
		t.Fatalf("expected one delta, got %#v", pending)
	}
	delta, ok := pending[0].(contracts.DeltaArtifactPublish)
	if !ok {
		t.Fatalf("expected DeltaArtifactPublish, got %#v", pending[0])
	}
	if delta.ArtifactCount != 2 || len(delta.Artifacts) != 2 {
		t.Fatalf("unexpected batched delta %#v", delta)
	}
}

func TestAppendPublishedArtifactDeltaSkipsEmptyArtifacts(t *testing.T) {
	var pending []contracts.AgentDelta

	appendPublishedArtifactDelta(&pending, contracts.QuerySession{
		ChatID: "chat_1",
		RunID:  "run_1",
	}, []any{
		map[string]any{},
	})

	if len(pending) != 0 {
		t.Fatalf("expected no delta for empty artifacts, got %#v", pending)
	}
}

func TestAppendPublishedArtifactDeltaUsesOnlyPublishedArtifacts(t *testing.T) {
	var pending []contracts.AgentDelta

	appendPublishedArtifactDelta(&pending, contracts.QuerySession{
		ChatID: "chat_1",
		RunID:  "run_1",
	}, []any{
		map[string]any{"artifactId": "artifact_1", "name": "report.md"},
		map[string]any{},
		map[string]any{"artifactId": "artifact_2", "name": "summary.txt"},
	})

	if len(pending) != 1 {
		t.Fatalf("expected one delta, got %#v", pending)
	}
	delta := pending[0].(contracts.DeltaArtifactPublish)
	if delta.ArtifactCount != 2 || len(delta.Artifacts) != 2 {
		t.Fatalf("expected only successful artifacts in delta, got %#v", delta)
	}
	if delta.Artifacts[0]["artifactId"] != "artifact_1" || delta.Artifacts[1]["artifactId"] != "artifact_2" {
		t.Fatalf("unexpected artifacts %#v", delta.Artifacts)
	}
}

func TestPrepareToolCall_InvokeAgentsReturnsBatchPrelude(t *testing.T) {
	stream := &llmRunStream{
		engine: &LLMAgentEngine{
			tools:    stubToolExecutor{defs: []api.ToolDetailResponse{invokeAgentsToolDefinition()}},
			frontend: frontendtools.NewDefaultRegistry(),
		},
		session: contracts.QuerySession{RunID: "run_1"},
		execCtx: &contracts.ExecutionContext{},
	}

	invocation, deltas, toolMsg := stream.prepareToolCall(openAIToolCall{
		ID:   "tool_1",
		Type: "function",
		Function: openAIFunctionCall{
			Name:      contracts.InvokeAgentsToolName,
			Arguments: `{"tasks":[{"subAgentKey":"writer","task":"Write a short summary","taskName":"总结"}]}`,
		},
	})
	if toolMsg != nil {
		t.Fatalf("expected no immediate tool message, got %#v", toolMsg)
	}
	if len(deltas) != 0 {
		t.Fatalf("expected no immediate error deltas, got %#v", deltas)
	}
	if invocation == nil || !invocation.awaitExternalResult {
		t.Fatalf("expected external-result invocation, got %#v", invocation)
	}
	if len(invocation.prelude) != 1 {
		t.Fatalf("expected one prelude delta, got %#v", invocation.prelude)
	}
	invoke, ok := invocation.prelude[0].(contracts.DeltaInvokeSubAgents)
	if !ok {
		t.Fatalf("expected DeltaInvokeSubAgents prelude, got %#v", invocation.prelude[0])
	}
	if invoke.MainToolID != "tool_1" || len(invoke.Tasks) != 1 || invoke.Tasks[0].SubAgentKey != "writer" || invoke.Tasks[0].TaskText != "Write a short summary" || invoke.Tasks[0].TaskName != "总结" || invoke.GroupID == "" {
		t.Fatalf("unexpected invoke delta %#v", invoke)
	}
}

func TestPrepareToolCall_InvokeAgentsRejectsTooManyTasks(t *testing.T) {
	stream := &llmRunStream{
		engine: &LLMAgentEngine{
			tools:    stubToolExecutor{defs: []api.ToolDetailResponse{invokeAgentsToolDefinition()}},
			frontend: frontendtools.NewDefaultRegistry(),
		},
		session: contracts.QuerySession{RunID: "run_1"},
		execCtx: &contracts.ExecutionContext{},
	}

	invocation, deltas, toolMsg := stream.prepareToolCall(openAIToolCall{
		ID:   "tool_1",
		Type: "function",
		Function: openAIFunctionCall{
			Name:      contracts.InvokeAgentsToolName,
			Arguments: `{"tasks":[{"subAgentKey":"a","task":"1"},{"subAgentKey":"b","task":"2"},{"subAgentKey":"c","task":"3"},{"subAgentKey":"d","task":"4"}]}`,
		},
	})
	if invocation != nil {
		t.Fatalf("expected no invocation, got %#v", invocation)
	}
	if len(deltas) != 1 {
		t.Fatalf("expected one tool error delta, got %#v", deltas)
	}
	result, ok := deltas[0].(contracts.DeltaToolResult)
	if !ok || result.Result.Error != "invalid_tool_arguments" {
		t.Fatalf("unexpected tool error %#v", deltas)
	}
	if toolMsg == nil {
		t.Fatal("expected tool message")
	}
}

func TestInjectToolResultAppendsToolMessageAndFinalAssistantContent(t *testing.T) {
	stream := &llmRunStream{
		engine: &LLMAgentEngine{
			tools:    stubToolExecutor{defs: []api.ToolDetailResponse{invokeAgentsToolDefinition()}},
			frontend: frontendtools.NewDefaultRegistry(),
		},
		session: contracts.QuerySession{RunID: "run_1"},
		execCtx: &contracts.ExecutionContext{},
		messages: []openAIMessage{
			{Role: "assistant", Content: "final child answer"},
		},
	}

	invocation, _, _ := stream.prepareToolCall(openAIToolCall{
		ID:   "tool_1",
		Type: "function",
		Function: openAIFunctionCall{
			Name:      contracts.InvokeAgentsToolName,
			Arguments: `{"tasks":[{"subAgentKey":"writer","task":"Write a short summary"}]}`,
		},
	})
	stream.queuedToolCalls = []*preparedToolInvocation{invocation}
	stream.activateNextToolCall()
	stream.pending = nil

	if !stream.InjectToolResult("tool_1", "done", false) {
		t.Fatal("expected InjectToolResult to match active agent_invoke tool")
	}
	if err := stream.invokeActiveToolCall(); err != nil {
		t.Fatalf("invokeActiveToolCall returned error: %v", err)
	}
	if len(stream.pending) != 1 {
		t.Fatalf("expected one pending tool result delta, got %#v", stream.pending)
	}
	result, ok := stream.pending[0].(contracts.DeltaToolResult)
	if !ok || result.ToolID != "tool_1" || result.Result.Output != "done" {
		t.Fatalf("unexpected pending tool result %#v", stream.pending)
	}
	if len(stream.messages) < 2 {
		t.Fatalf("expected tool message to be appended, got %#v", stream.messages)
	}
	last := stream.messages[len(stream.messages)-1]
	if last.Role != "tool" || last.ToolCallID != "tool_1" || last.Content != "done" {
		t.Fatalf("unexpected tool message %#v", last)
	}
	if content, ok := stream.FinalAssistantContent(); !ok || content != "final child answer" {
		t.Fatalf("expected FinalAssistantContent to return last assistant text, got %q %v", content, ok)
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
	if result.Result.Error != "invalid_tool_arguments" || !strings.Contains(result.Result.Output, "options is required for select and multi-select questions") {
		t.Fatalf("unexpected tool result %#v", result)
	}
	toolContent, _ := toolMsg.Content.(string)
	if toolMsg == nil || !strings.Contains(toolContent, "options is required for select and multi-select questions") {
		t.Fatalf("unexpected tool message %#v", toolMsg)
	}
}

func TestPrepareToolCall_BashDescriptionIsRequired(t *testing.T) {
	tool := bashToolDefinition()
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
			Name:      "_bash_",
			Arguments: `{"command":"chmod 777 ~/a.sh"}`,
		},
	})
	if invocation != nil {
		t.Fatalf("expected no invocation, got %#v", invocation)
	}
	if len(deltas) != 1 {
		t.Fatalf("expected one tool error delta, got %#v", deltas)
	}
	result, ok := deltas[0].(contracts.DeltaToolResult)
	if !ok || result.Result.Error != "invalid_tool_arguments" || !strings.Contains(result.Result.Output, "description is required") {
		t.Fatalf("unexpected bash invalid-args result %#v", deltas)
	}
	toolContent, _ := toolMsg.Content.(string)
	if toolMsg == nil || !strings.Contains(toolContent, "description is required") {
		t.Fatalf("unexpected tool message %#v", toolMsg)
	}
}

func TestBashHITLApprovalUsesAwaitingForAllViewports(t *testing.T) {
	tests := []struct {
		name                     string
		rule                     hitl.FlatRule
		initialCommand           string
		parsedCommand            hitl.CommandComponents
		submitParams             api.SubmitParams
		expectedCommand          string
		expectedView             string
		expectedKey              string
		expectedInitialPayload   map[string]any
		expectedSubmittedPayload map[string]any
		expectedAnswerAction     string
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
			submitParams: encodedSubmitParams(t, []map[string]any{
				map[string]any{
					"id":       "tool_1",
					"decision": "approve",
				},
			}),
			expectedCommand: "git push origin main",
			expectedView:    "",
			expectedKey:     "",
		},
		{
			name: "leave html viewport override",
			rule: hitl.FlatRule{
				Match:        "create-leave",
				Level:        1,
				Title:        "mock 请假申请",
				ViewportType: "html",
				ViewportKey:  "leave_form",
			},
			initialCommand: sampleLeaveCommand(3),
			parsedCommand: hitl.CommandComponents{
				BaseCommand: "mock",
				Tokens:      []string{"create-leave", "--payload", `{"applicant_id":"E1001","days":3,"department_id":"engineering","end_date":"2026-04-22","leave_type":"annual","reason":"family_trip","start_date":"2026-04-20"}`},
			},
			submitParams: encodedSubmitParams(t, []map[string]any{
				{
					"id":      "form-1",
					"payload": sampleLeavePayload(2),
				},
			}),
			expectedCommand:          sampleLeaveCommand(2),
			expectedView:             "html",
			expectedKey:              "leave_form",
			expectedInitialPayload:   sampleLeavePayload(3),
			expectedSubmittedPayload: sampleLeavePayload(2),
			expectedAnswerAction:     "submit",
		},
		{
			name: "expense html viewport override",
			rule: hitl.FlatRule{
				Match:        "expense add",
				Level:        1,
				ViewportType: "html",
				ViewportKey:  "expense_form",
			},
			initialCommand: sampleExpenseCommand(1280.5),
			parsedCommand: hitl.CommandComponents{
				BaseCommand: "mock",
				Tokens:      []string{"expense", "add", "--payload", `{"employee":{"id":"E1001","name":"张三"},"department":{"code":"engineering","name":"工程部"},"expense_type":"travel","currency":"CNY","items":[{"amount":1280.5,"category":"transport","description":"flight","invoice_id":"INV-001","occurred_on":"2026-04-10"}],"submitted_at":"2026-04-14T10:30:00+08:00","total_amount":1280.5}`},
			},
			submitParams: encodedSubmitParams(t, []map[string]any{
				{
					"id": "form-1",
					"payload": map[string]any{
						"employee":     map[string]any{"id": "E1001", "name": "张三"},
						"department":   map[string]any{"code": "engineering", "name": "工程部"},
						"expense_type": "travel",
						"currency":     "CNY",
						"items": []any{
							map[string]any{
								"amount":      640.25,
								"category":    "transport",
								"description": "flight",
								"invoice_id":  "INV-001",
								"occurred_on": "2026-04-10",
							},
						},
						"submitted_at": "2026-04-14T10:30:00+08:00",
						"total_amount": 640.25,
					},
				},
			}),
			expectedCommand: canonicalExpenseCommand(640.25),
			expectedView:    "html",
			expectedKey:     "expense_form",
			expectedSubmittedPayload: map[string]any{
				"employee":     map[string]any{"id": "E1001", "name": "张三"},
				"department":   map[string]any{"code": "engineering", "name": "工程部"},
				"expense_type": "travel",
				"currency":     "CNY",
				"items": []any{
					map[string]any{
						"amount":      640.25,
						"category":    "transport",
						"description": "flight",
						"invoice_id":  "INV-001",
						"occurred_on": "2026-04-10",
					},
				},
				"submitted_at": "2026-04-14T10:30:00+08:00",
				"total_amount": 640.25,
			},
			expectedInitialPayload: map[string]any{
				"employee":     map[string]any{"id": "E1001", "name": "张三"},
				"department":   map[string]any{"code": "engineering", "name": "工程部"},
				"expense_type": "travel",
				"currency":     "CNY",
				"items": []any{
					map[string]any{
						"amount":      1280.5,
						"category":    "transport",
						"description": "flight",
						"invoice_id":  "INV-001",
						"occurred_on": "2026-04-10",
					},
				},
				"submitted_at": "2026-04-14T10:30:00+08:00",
				"total_amount": 1280.5,
			},
			expectedAnswerAction: "submit",
		},
		{
			name: "procurement html viewport override",
			rule: hitl.FlatRule{
				Match:        "procurement create",
				Level:        1,
				ViewportType: "html",
				ViewportKey:  "procurement_form",
			},
			initialCommand: sampleProcurementCommand("Shanghai"),
			parsedCommand: hitl.CommandComponents{
				BaseCommand: "mock",
				Tokens:      []string{"procurement", "create", "--payload", `{"delivery_city":"Shanghai","requester_id":"E1001"}`},
			},
			submitParams: encodedSubmitParams(t, []map[string]any{
				{
					"id": "form-1",
					"payload": map[string]any{
						"delivery_city": "Hangzhou",
						"requester_id":  "E1001",
					},
				},
			}),
			expectedCommand:          sampleProcurementCommand("Hangzhou"),
			expectedView:             "html",
			expectedKey:              "procurement_form",
			expectedInitialPayload:   map[string]any{"delivery_city": "Shanghai", "requester_id": "E1001"},
			expectedSubmittedPayload: map[string]any{"delivery_city": "Hangzhou", "requester_id": "E1001"},
			expectedAnswerAction:     "submit",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			executor := &recordingToolExecutor{
				defs: []api.ToolDetailResponse{bashToolDefinition()},
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
				toolName: "_bash_",
				args: map[string]any{
					"command":     tc.initialCommand,
					"description": "执行命令用途说明",
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
			expectedMode := "approval"
			if tc.expectedInitialPayload != nil {
				expectedMode = "form"
			}
			if awaitAsk.Mode != expectedMode || awaitAsk.ViewportType != tc.expectedView || awaitAsk.ViewportKey != tc.expectedKey {
				t.Fatalf("unexpected await ask %#v", awaitAsk)
			}
			if tc.expectedInitialPayload != nil {
				if len(awaitAsk.Forms) != 1 {
					t.Fatalf("expected one form awaiting item, got %#v", awaitAsk)
				}
				form := awaitAsk.Forms[0].(map[string]any)
				if _, ok := form["command"]; ok {
					t.Fatalf("did not expect form command in awaiting.ask payload, got %#v", form)
				}
				formPayload, _ := form["payload"].(map[string]any)
				if !reflect.DeepEqual(formPayload, tc.expectedInitialPayload) {
					t.Fatalf("expected form payload %#v, got %#v", tc.expectedInitialPayload, awaitAsk)
				}
				if title, _ := form["title"].(string); tc.expectedKey == "leave_form" && title != "mock 请假申请" {
					t.Fatalf("expected form title in awaiting.ask payload, got %#v", form)
				}
				if tc.expectedKey != "leave_form" {
					if _, ok := form["title"]; ok {
						t.Fatalf("did not expect title for non-leave form, got %#v", form)
					}
				}
				if len(awaitAsk.Approvals) != 0 {
					t.Fatalf("expected form await to omit approvals, got %#v", awaitAsk.Approvals)
				}
			} else {
				approvals := awaitAsk.Approvals
				if len(approvals) != 1 {
					t.Fatalf("expected one approval item, got %#v", awaitAsk.Approvals)
				}
				firstApproval, ok := approvals[0].(map[string]any)
				if !ok {
					t.Fatalf("expected approval object, got %#v", approvals[0])
				}
				if firstApproval["command"] != tc.initialCommand || firstApproval["id"] != "tool_1" {
					t.Fatalf("expected approval item to use original command, got %#v", firstApproval)
				}
				if firstApproval["description"] != "执行命令用途说明" {
					t.Fatalf("expected approval description from tool args, got %#v", firstApproval)
				}
				if _, ok := firstApproval["level"]; ok {
					t.Fatalf("did not expect level in approval awaiting.ask payload, got %#v", firstApproval)
				}
				options, _ := firstApproval["options"].([]any)
				if len(options) != 3 {
					t.Fatalf("expected 3 approval options, got %#v", firstApproval)
				}
				if option, ok := options[0].(map[string]any); !ok || option["decision"] != "approve" {
					t.Fatalf("expected approval options to use decision field, got %#v", options)
				}
				if firstApproval["allowFreeText"] != true || firstApproval["freeTextPlaceholder"] == "" {
					t.Fatalf("expected free text approval metadata, got %#v", firstApproval)
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
			if executor.invocations[0].name != "_bash_" {
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
						if tc.expectedAnswerAction != "" {
							forms, _ := typed.Answer["forms"].([]map[string]any)
							if len(forms) == 0 || forms[0]["action"] != tc.expectedAnswerAction {
								t.Fatalf("expected awaiting.answer action %q, got %#v", tc.expectedAnswerAction, typed.Answer)
							}
						}
					}
				case contracts.DeltaToolResult:
					if typed.ToolName == "_bash_" {
						foundOriginalResult = true
						if tc.expectedSubmittedPayload != nil {
							if typed.Result.HITL["mode"] != "form" || typed.Result.HITL["decision"] != "approve" {
								t.Fatalf("expected form approve HITL metadata, got %#v", typed.Result.HITL)
							}
							if !reflect.DeepEqual(typed.Result.HITL["submittedPayload"], tc.expectedSubmittedPayload) {
								t.Fatalf("expected submitted payload %#v, got %#v", tc.expectedSubmittedPayload, typed.Result.HITL["submittedPayload"])
							}
						}
					}
				}
			}
			if !foundRequestSubmit || !foundAwaitingAnswer || !foundOriginalResult {
				t.Fatalf("expected submit/answer/results deltas, got %#v", stream.pending)
			}
			if len(stream.messages) < 2 {
				t.Fatalf("expected tool result and HITL summary messages, got %#v", stream.messages)
			}
			toolMsg := stream.messages[len(stream.messages)-2]
			if toolMsg.Role != "tool" || toolMsg.ToolCallID != "tool_1" || toolMsg.Content != "executed" {
				t.Fatalf("expected pure tool output before HITL summary, got %#v", toolMsg)
			}
			hitlNotice := stream.messages[len(stream.messages)-1]
			if hitlNotice.Role != "user" {
				t.Fatalf("expected HITL summary to be appended as user message, got %#v", hitlNotice)
			}
			noticeText, _ := hitlNotice.Content.(string)
			if tc.expectedSubmittedPayload != nil {
				if !strings.Contains(noticeText, "[HITL] ") || !strings.Contains(noticeText, " → approve") || !strings.Contains(noticeText, "\n  提交参数: ") {
					t.Fatalf("expected form HITL summary content, got %#v", hitlNotice)
				}
			} else if !strings.Contains(noticeText, "[HITL] git push origin main → approve") {
				t.Fatalf("expected HITL summary content, got %#v", hitlNotice)
			}
		})
	}
}

func TestAwaitHITLSubmitAndExecute_RejectEmitsCancelledAnswer(t *testing.T) {
	runControl := contracts.NewRunControl(context.Background(), "run_1")
	stream := &llmRunStream{
		ctx: context.Background(),
		engine: &LLMAgentEngine{
			tools:    &recordingToolExecutor{defs: []api.ToolDetailResponse{bashToolDefinition()}},
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
			toolName: "_bash_",
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
			"approvals": []any{
				map[string]any{
					"id":                  "tool_1",
					"command":             "docker rmi nginx:latest",
					"description":         "删除镜像",
					"options":             buildApprovalOptions(),
					"allowFreeText":       true,
					"freeTextPlaceholder": "可选：填写理由",
				},
			},
		},
	}
	runControl.ExpectSubmit(contracts.AwaitingSubmitContext{
		AwaitingID: stream.hitlAwaitingID,
		Mode:       "approval",
		ItemCount:  1,
	})
	ack := runControl.ResolveSubmit(api.SubmitRequest{
		RunID:      "run_1",
		AwaitingID: stream.hitlAwaitingID,
		Params:     encodedSubmitParams(t, []map[string]any{{"id": "tool_1", "decision": "reject", "reason": "风险过高"}}),
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
				approvals, ok := typed.Answer["approvals"].([]map[string]any)
				if !ok || len(approvals) != 1 || approvals[0]["decision"] != "reject" || approvals[0]["reason"] != "风险过高" {
					t.Fatalf("unexpected reject approvals %#v", typed.Answer)
				}
			}
		case contracts.DeltaToolResult:
			if typed.ToolName == "_bash_" {
				foundResult = true
				if typed.Result.Error != "user_rejected" {
					t.Fatalf("expected user_rejected tool result, got %#v", typed.Result)
				}
				if typed.Result.HITL["decision"] != "reject" || typed.Result.HITL["reason"] != "风险过高" {
					t.Fatalf("expected reject HITL metadata on tool result, got %#v", typed.Result)
				}
			}
		}
	}
	if !foundAnswer || !foundResult {
		t.Fatalf("expected reject answer and tool result, got %#v", stream.pending)
	}
	if len(stream.messages) < 2 {
		t.Fatalf("expected reject flow to append tool result and HITL summary, got %#v", stream.messages)
	}
	toolMsg := stream.messages[len(stream.messages)-2]
	if toolMsg.Role != "tool" || toolMsg.ToolCallID != "tool_1" {
		t.Fatalf("expected reject tool result message before notice, got %#v", toolMsg)
	}
	hitlNotice := stream.messages[len(stream.messages)-1]
	noticeText, _ := hitlNotice.Content.(string)
	if hitlNotice.Role != "user" || !strings.Contains(noticeText, `[HITL] docker rmi nginx:latest → reject（风险过高）`) {
		t.Fatalf("expected reject HITL summary, got %#v", hitlNotice)
	}
}

func TestAwaitHITLSubmitAndExecute_FormRejectEmitsHITLMetadataAndSummary(t *testing.T) {
	executor := &recordingToolExecutor{defs: []api.ToolDetailResponse{bashToolDefinition()}}
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
		toolName: "_bash_",
		args: map[string]any{
			"command": sampleLeaveCommand(3),
		},
	}
	result := hitl.InterceptResult{
		Intercepted: true,
		Rule: hitl.FlatRule{
			Match:        "create-leave",
			Level:        1,
			ViewportType: "html",
			ViewportKey:  "leave_form",
		},
		ParsedCommand: hitl.CommandComponents{
			BaseCommand: "mock",
			Tokens:      []string{"create-leave", "--payload", `{"applicant_id":"E1001","days":3,"department_id":"engineering","end_date":"2026-04-22","leave_type":"annual","reason":"family_trip","start_date":"2026-04-20"}`},
		},
	}
	if err := stream.emitHITLConfirmDeltas(invocation, result); err != nil {
		t.Fatalf("emitHITLConfirmDeltas returned error: %v", err)
	}
	ack := runControl.ResolveSubmit(api.SubmitRequest{
		RunID:      "run_1",
		AwaitingID: stream.hitlAwaitingID,
		Params: encodedSubmitParams(t, []map[string]any{
			{"id": "form-1", "reason": "用户取消"},
		}),
	})
	if !ack.Accepted {
		t.Fatalf("expected submit to be accepted, got %#v", ack)
	}
	if err := stream.awaitHITLSubmitAndExecute(); err != nil {
		t.Fatalf("awaitHITLSubmitAndExecute returned error: %v", err)
	}
	if len(executor.invocations) != 0 {
		t.Fatalf("did not expect rejected form command to execute, got %#v", executor.invocations)
	}

	foundResult := false
	for _, delta := range stream.pending {
		typed, ok := delta.(contracts.DeltaToolResult)
		if !ok || typed.ToolName != "_bash_" {
			continue
		}
		foundResult = true
		if typed.Result.Error != "user_rejected" {
			t.Fatalf("expected user_rejected result, got %#v", typed.Result)
		}
		if typed.Result.HITL["mode"] != "form" || typed.Result.HITL["decision"] != "reject" || typed.Result.HITL["reason"] != "用户取消" {
			t.Fatalf("expected form reject HITL metadata, got %#v", typed.Result.HITL)
		}
		if _, ok := typed.Result.HITL["submittedPayload"]; ok {
			t.Fatalf("did not expect submitted payload for form reject, got %#v", typed.Result.HITL)
		}
	}
	if !foundResult {
		t.Fatalf("expected rejected tool result, got %#v", stream.pending)
	}
	if len(stream.messages) < 2 {
		t.Fatalf("expected form reject tool message and HITL summary, got %#v", stream.messages)
	}
	noticeText, _ := stream.messages[len(stream.messages)-1].Content.(string)
	if !strings.Contains(noticeText, `[HITL] `) || !strings.Contains(noticeText, ` → reject（用户取消）`) || strings.Contains(noticeText, "提交参数:") {
		t.Fatalf("expected form reject HITL summary without submitted payload, got %#v", stream.messages[len(stream.messages)-1])
	}
}

func TestAwaitHITLSubmitAndExecute_FormPayloadRebuildFailureEmitsRejectHITLMetadata(t *testing.T) {
	executor := &recordingToolExecutor{defs: []api.ToolDetailResponse{bashToolDefinition()}}
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
		toolName: "_bash_",
		args: map[string]any{
			"command": "mock create-leave",
		},
	}
	result := hitl.InterceptResult{
		Intercepted: true,
		Rule: hitl.FlatRule{
			Match:        "create-leave",
			Level:        1,
			ViewportType: "html",
			ViewportKey:  "leave_form",
		},
		ParsedCommand: hitl.CommandComponents{
			BaseCommand: "mock",
			Tokens:      []string{"create-leave"},
		},
	}
	if err := stream.emitHITLConfirmDeltas(invocation, result); err != nil {
		t.Fatalf("emitHITLConfirmDeltas returned error: %v", err)
	}
	ack := runControl.ResolveSubmit(api.SubmitRequest{
		RunID:      "run_1",
		AwaitingID: stream.hitlAwaitingID,
		Params: encodedSubmitParams(t, []map[string]any{
			{"id": "form-1", "payload": sampleLeavePayload(2)},
		}),
	})
	if !ack.Accepted {
		t.Fatalf("expected submit to be accepted, got %#v", ack)
	}
	if err := stream.awaitHITLSubmitAndExecute(); err != nil {
		t.Fatalf("awaitHITLSubmitAndExecute returned error: %v", err)
	}
	if len(executor.invocations) != 0 {
		t.Fatalf("did not expect invalid form payload command to execute, got %#v", executor.invocations)
	}

	foundResult := false
	for _, delta := range stream.pending {
		typed, ok := delta.(contracts.DeltaToolResult)
		if !ok || typed.ToolName != "_bash_" {
			continue
		}
		foundResult = true
		if typed.Result.Error != "frontend_submit_invalid_payload" {
			t.Fatalf("expected invalid payload result, got %#v", typed.Result)
		}
		if typed.Result.HITL["mode"] != "form" || typed.Result.HITL["decision"] != "reject" {
			t.Fatalf("expected form reject HITL metadata, got %#v", typed.Result.HITL)
		}
		reason, _ := typed.Result.HITL["reason"].(string)
		if !strings.Contains(reason, "original command does not contain --payload") {
			t.Fatalf("expected rebuild error reason, got %#v", typed.Result.HITL)
		}
		if _, ok := typed.Result.HITL["submittedPayload"]; ok {
			t.Fatalf("did not expect submitted payload for rebuild failure, got %#v", typed.Result.HITL)
		}
	}
	if !foundResult {
		t.Fatalf("expected invalid payload tool result, got %#v", stream.pending)
	}
	if len(stream.messages) < 2 {
		t.Fatalf("expected rebuild failure tool message and HITL summary, got %#v", stream.messages)
	}
	noticeText, _ := stream.messages[len(stream.messages)-1].Content.(string)
	if !strings.Contains(noticeText, `mock create-leave → reject（original command does not contain --payload）`) || strings.Contains(noticeText, "提交参数:") {
		t.Fatalf("expected rebuild failure HITL summary without submitted payload, got %#v", stream.messages[len(stream.messages)-1])
	}
}

func TestAwaitHITLApprovalBatchAndContinue_HostUsesUnifiedBashToolName(t *testing.T) {
	runControl := contracts.NewRunControl(context.Background(), "run_1")
	executor := &recordingToolExecutor{
		defs:   []api.ToolDetailResponse{bashToolDefinition()},
		result: contracts.ToolExecutionResult{Output: "approved", ExitCode: 0},
	}
	stream := &llmRunStream{
		ctx: context.Background(),
		engine: &LLMAgentEngine{
			tools:    executor,
			frontend: frontendtools.NewDefaultRegistry(),
		},
		session: contracts.QuerySession{
			RequestID:             "req_1",
			ChatID:                "chat_1",
			RunID:                 "run_1",
			AgentHasSandboxConfig: false,
		},
		runControl: runControl,
		execCtx: &contracts.ExecutionContext{
			Budget: contracts.Budget{Tool: contracts.RetryPolicy{TimeoutMs: 100}},
			Session: contracts.QuerySession{
				RunID:                 "run_1",
				AgentHasSandboxConfig: false,
			},
		},
		checker: commandResultChecker{
			results: map[string]hitl.InterceptResult{
				"git status": {
					Intercepted:     true,
					Rule:            hitl.FlatRule{Level: 1, ViewportType: "builtin", ViewportKey: "confirm_dialog", RuleKey: "repo::git"},
					OriginalCommand: "git status",
				},
			},
			tools: map[string]api.ToolDetailResponse{
				strings.ToLower(bashToolDefinition().Name): bashToolDefinition(),
			},
		},
		queuedToolCalls: []*preparedToolInvocation{{
			toolID:   "tool_1",
			toolName: "_bash_",
			args: map[string]any{
				"command":     "git status",
				"description": "查看仓库状态",
			},
		}},
	}
	if !stream.prepareQueuedBashApprovalBatch() {
		t.Fatal("expected host bash HITL batch to be prepared")
	}
	ask := stream.pending[0].(contracts.DeltaAwaitAsk)
	runControl.ExpectSubmit(contracts.AwaitingSubmitContext{
		AwaitingID: ask.AwaitingID,
		Mode:       "approval",
		ItemCount:  1,
	})

	done := make(chan error, 1)
	go func() {
		done <- stream.awaitHITLApprovalBatchAndContinue()
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if stream.execCtx.CurrentToolName == "_bash_" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if stream.execCtx.CurrentToolName != "_bash_" {
		t.Fatalf("expected CurrentToolName to be _bash_ while awaiting approval, got %q", stream.execCtx.CurrentToolName)
	}
	ack := runControl.ResolveSubmit(api.SubmitRequest{
		RunID:      "run_1",
		AwaitingID: ask.AwaitingID,
		Params:     encodedSubmitParams(t, []map[string]any{{"id": "tool_1", "decision": "approve"}}),
	})
	if !ack.Accepted {
		t.Fatalf("expected submit to be accepted, got %#v", ack)
	}
	if err := <-done; err != nil {
		t.Fatalf("awaitHITLApprovalBatchAndContinue returned error: %v", err)
	}
	for len(stream.queuedToolCalls) > 0 {
		stream.activateNextToolCall()
		if err := stream.invokeActiveToolCall(); err != nil {
			t.Fatalf("invokeActiveToolCall returned error: %v", err)
		}
	}
	if len(executor.invocations) != 1 || executor.invocations[0].name != "_bash_" {
		t.Fatalf("expected host HITL flow to invoke unified _bash_, got %#v", executor.invocations)
	}
	foundResult := false
	for _, delta := range stream.pending {
		if typed, ok := delta.(contracts.DeltaToolResult); ok && typed.ToolName == "_bash_" {
			foundResult = true
		}
	}
	if !foundResult {
		t.Fatalf("expected approved host HITL flow to emit _bash_ tool result, got %#v", stream.pending)
	}
}

func TestPrepareQueuedBashApprovalBatch_AppendsSingleSummaryAfterAllApprovedResults(t *testing.T) {
	executor := &recordingToolExecutor{
		defs: []api.ToolDetailResponse{bashToolDefinition()},
		result: contracts.ToolExecutionResult{
			Output:   "ok",
			ExitCode: 0,
		},
	}
	stream := &llmRunStream{
		ctx: context.Background(),
		engine: &LLMAgentEngine{
			tools: executor,
		},
		session: contracts.QuerySession{
			RequestID: "req_1",
			ChatID:    "chat_1",
			RunID:     "run_1",
		},
		runControl: contracts.NewRunControl(context.Background(), "run_1"),
		execCtx: &contracts.ExecutionContext{
			Budget: contracts.Budget{Tool: contracts.RetryPolicy{TimeoutMs: 50}},
		},
		checker: commandResultChecker{
			results: map[string]hitl.InterceptResult{
				"chmod 777 ~/a.sh": {
					Intercepted:     true,
					Rule:            hitl.FlatRule{Level: 1, ViewportType: "builtin", ViewportKey: "confirm_dialog", RuleKey: "dangerous-commands::chmod"},
					OriginalCommand: "chmod 777 ~/a.sh",
				},
				"chmod 777 ~/b.sh": {
					Intercepted:     true,
					Rule:            hitl.FlatRule{Level: 1, ViewportType: "builtin", ViewportKey: "confirm_dialog", RuleKey: "dangerous-commands::chmod"},
					OriginalCommand: "chmod 777 ~/b.sh",
				},
				"chmod 777 ~/c.sh": {
					Intercepted:     true,
					Rule:            hitl.FlatRule{Level: 1, ViewportType: "builtin", ViewportKey: "confirm_dialog", RuleKey: "dangerous-commands::chmod"},
					OriginalCommand: "chmod 777 ~/c.sh",
				},
			},
			tools: map[string]api.ToolDetailResponse{
				strings.ToLower(bashToolDefinition().Name): bashToolDefinition(),
			},
		},
		queuedToolCalls: []*preparedToolInvocation{
			{toolID: "tool_1", toolName: "_bash_", args: map[string]any{"command": "chmod 777 ~/a.sh", "description": "放开 a.sh 权限"}},
			{toolID: "tool_2", toolName: "_bash_", args: map[string]any{"command": "chmod 777 ~/b.sh", "description": "放开 b.sh 权限"}},
			{toolID: "tool_3", toolName: "_bash_", args: map[string]any{"command": "chmod 777 ~/c.sh", "description": "放开 c.sh 权限"}},
		},
	}
	var recordedApproval *chat.StepApproval
	stream.onApprovalSummary = func(approval chat.StepApproval) {
		copied := approval
		copied.Decisions = append([]chat.StepApprovalDecision(nil), approval.Decisions...)
		recordedApproval = &copied
	}

	if !stream.prepareQueuedBashApprovalBatch() {
		t.Fatal("expected batch approval await to be prepared")
	}
	ask := stream.pending[0].(contracts.DeltaAwaitAsk)
	approvalItems, _ := ask.Approvals[0].(map[string]any)
	if approvalItems["ruleKey"] != "dangerous-commands::chmod" {
		t.Fatalf("expected approval ask item to include ruleKey, got %#v", ask.Approvals)
	}
	ack := stream.runControl.ResolveSubmit(api.SubmitRequest{
		RunID:      "run_1",
		AwaitingID: ask.AwaitingID,
		Params: encodedSubmitParams(t, []map[string]any{
			{"id": "tool_1", "decision": "approve"},
			{"id": "tool_2", "decision": "approve"},
			{"id": "tool_3", "decision": "approve"},
		}),
	})
	if !ack.Accepted {
		t.Fatalf("expected submit to be accepted, got %#v", ack)
	}
	if err := stream.awaitHITLApprovalBatchAndContinue(); err != nil {
		t.Fatalf("awaitHITLApprovalBatchAndContinue returned error: %v", err)
	}
	for len(stream.queuedToolCalls) > 0 {
		stream.activateNextToolCall()
		if err := stream.invokeActiveToolCall(); err != nil {
			t.Fatalf("invokeActiveToolCall returned error: %v", err)
		}
	}

	if len(stream.messages) != 4 {
		t.Fatalf("expected tool, tool, tool, user summary messages, got %#v", stream.messages)
	}
	for index := 0; index < 3; index++ {
		if stream.messages[index].Role != "tool" {
			t.Fatalf("expected message %d to be tool result, got %#v", index, stream.messages[index])
		}
	}
	summary := stream.messages[3]
	if summary.Role != "user" {
		t.Fatalf("expected final message to be user summary, got %#v", summary)
	}
	text, _ := summary.Content.(string)
	if !strings.Contains(text, `[HITL] 审批结果：`) ||
		!strings.Contains(text, `1. chmod 777 ~/a.sh → approve`) ||
		!strings.Contains(text, `2. chmod 777 ~/b.sh → approve`) ||
		!strings.Contains(text, `3. chmod 777 ~/c.sh → approve`) {
		t.Fatalf("unexpected all-approved summary %#v", summary)
	}
	if recordedApproval == nil {
		t.Fatal("expected approval batch to be recorded")
	}
	if recordedApproval.Summary != text {
		t.Fatalf("expected recorded approval summary to match user message, got %#v", recordedApproval)
	}
	if len(recordedApproval.Decisions) != 3 || recordedApproval.Decisions[2].Decision != "approve" {
		t.Fatalf("expected recorded approval decisions, got %#v", recordedApproval)
	}
	if recordedApproval.Decisions[0].RuleKey != "dangerous-commands::chmod" {
		t.Fatalf("expected recorded approval decisions to include ruleKey, got %#v", recordedApproval)
	}
}

func TestPrepareQueuedBashApprovalBatch_MergesAllBuiltinApprovalsInSingleAwait(t *testing.T) {
	executor := &recordingToolExecutor{
		defs: []api.ToolDetailResponse{bashToolDefinition()},
		result: contracts.ToolExecutionResult{
			Output:   "ok",
			ExitCode: 0,
		},
	}
	stream := &llmRunStream{
		ctx: context.Background(),
		engine: &LLMAgentEngine{
			tools: executor,
		},
		session: contracts.QuerySession{
			RequestID: "req_1",
			ChatID:    "chat_1",
			RunID:     "run_1",
		},
		runControl: contracts.NewRunControl(context.Background(), "run_1"),
		execCtx: &contracts.ExecutionContext{
			Budget: contracts.Budget{Tool: contracts.RetryPolicy{TimeoutMs: 50}},
		},
		checker: commandResultChecker{
			results: map[string]hitl.InterceptResult{
				"chmod 777 ~/a.sh": {
					Intercepted:     true,
					Rule:            hitl.FlatRule{Level: 1, ViewportType: "builtin", ViewportKey: "confirm_dialog"},
					OriginalCommand: "chmod 777 ~/a.sh",
				},
				"chmod 777 ~/b.sh": {
					Intercepted:     true,
					Rule:            hitl.FlatRule{Level: 2, ViewportType: "builtin", ViewportKey: "confirm_dialog"},
					OriginalCommand: "chmod 777 ~/b.sh",
				},
				"chmod 777 ~/c.sh": {
					Intercepted:     true,
					Rule:            hitl.FlatRule{Level: 1, ViewportType: "builtin", ViewportKey: "confirm_dialog"},
					OriginalCommand: "chmod 777 ~/c.sh",
				},
			},
			tools: map[string]api.ToolDetailResponse{
				strings.ToLower(bashToolDefinition().Name): bashToolDefinition(),
			},
		},
		queuedToolCalls: []*preparedToolInvocation{
			{toolID: "tool_1", toolName: "_bash_", args: map[string]any{"command": "chmod 777 ~/a.sh", "description": "放开 a.sh 权限"}},
			{toolID: "tool_2", toolName: "_bash_", args: map[string]any{"command": "chmod 777 ~/b.sh", "description": "放开 b.sh 权限"}},
			{toolID: "tool_3", toolName: "_bash_", args: map[string]any{"command": "chmod 777 ~/c.sh", "description": "放开 c.sh 权限"}},
		},
	}

	if !stream.prepareQueuedBashApprovalBatch() {
		t.Fatal("expected batch approval await to be prepared")
	}
	if stream.hitlPendingBatch == nil {
		t.Fatal("expected hitlPendingBatch to be populated")
	}

	ask, ok := stream.pending[0].(contracts.DeltaAwaitAsk)
	if !ok {
		t.Fatalf("expected first pending delta to be await ask, got %#v", stream.pending)
	}
	if ask.Mode != "approval" || len(ask.Approvals) != 3 {
		t.Fatalf("unexpected batch await ask %#v", ask)
	}
	for index, rawApproval := range ask.Approvals {
		approval, ok := rawApproval.(map[string]any)
		if !ok {
			t.Fatalf("expected approval object, got %#v", rawApproval)
		}
		expectedID := fmt.Sprintf("tool_%d", index+1)
		if approval["id"] != expectedID {
			t.Fatalf("expected approval[%d] id %q, got %#v", index, expectedID, approval)
		}
		if _, ok := approval["level"]; ok {
			t.Fatalf("did not expect level in batch approval payload, got %#v", approval)
		}
		if approval["description"] == "" {
			t.Fatalf("expected approval description, got %#v", approval)
		}
		options, _ := approval["options"].([]any)
		if len(options) != 3 || approval["allowFreeText"] != true {
			t.Fatalf("expected approval options and free text metadata, got %#v", approval)
		}
	}

	ack := stream.runControl.ResolveSubmit(api.SubmitRequest{
		RunID:      "run_1",
		AwaitingID: ask.AwaitingID,
		Params: encodedSubmitParams(t, []map[string]any{
			{"id": "tool_1", "decision": "approve"},
			{"id": "tool_2", "decision": "approve"},
			{"id": "tool_3", "decision": "reject", "reason": "风险过高"},
		}),
	})
	if !ack.Accepted {
		t.Fatalf("expected submit to be accepted, got %#v", ack)
	}
	if err := stream.awaitHITLApprovalBatchAndContinue(); err != nil {
		t.Fatalf("awaitHITLApprovalBatchAndContinue returned error: %v", err)
	}

	for len(stream.queuedToolCalls) > 0 {
		stream.activateNextToolCall()
		if err := stream.invokeActiveToolCall(); err != nil {
			t.Fatalf("invokeActiveToolCall returned error: %v", err)
		}
	}

	if len(executor.invocations) != 2 {
		t.Fatalf("expected 2 approved bash invocations, got %#v", executor.invocations)
	}
	if executor.invocations[0].args["command"] != "chmod 777 ~/a.sh" || executor.invocations[1].args["command"] != "chmod 777 ~/b.sh" {
		t.Fatalf("unexpected executed commands %#v", executor.invocations)
	}

	var answer map[string]any
	approvalAskCount := 0
	rejectedCount := 0
	approvedResults := 0
	for _, delta := range stream.pending {
		switch typed := delta.(type) {
		case contracts.DeltaAwaitAsk:
			if typed.AwaitingID == ask.AwaitingID && typed.Mode == "approval" {
				approvalAskCount++
			}
		case contracts.DeltaAwaitingAnswer:
			if typed.AwaitingID == ask.AwaitingID {
				answer = typed.Answer
			}
		case contracts.DeltaToolResult:
			if typed.Result.Error == "user_rejected" {
				rejectedCount++
				continue
			}
			if typed.ToolName == "_bash_" {
				approvedResults++
				if typed.Result.HITL["decision"] != "approve" || typed.Result.HITL["awaitingId"] != ask.AwaitingID {
					t.Fatalf("expected approved tool result to include HITL metadata, got %#v", typed.Result)
				}
			}
		}
	}
	if answer == nil {
		t.Fatalf("expected awaiting.answer delta, got %#v", stream.pending)
	}
	approvals, ok := answer["approvals"].([]map[string]any)
	if !ok || len(approvals) != 3 {
		t.Fatalf("expected 3 approval answers, got %#v", answer)
	}
	if approvals[2]["decision"] != "reject" {
		t.Fatalf("expected third approval decision to be reject, got %#v", approvals)
	}
	if approvals[2]["reason"] != "风险过高" {
		t.Fatalf("expected third approval reason to be preserved, got %#v", approvals)
	}
	if rejectedCount != 1 {
		t.Fatalf("expected exactly one rejected tool result, got %#v", stream.pending)
	}
	if approvedResults != 2 {
		t.Fatalf("expected exactly two approved tool results with HITL metadata, got %#v", stream.pending)
	}
	if approvalAskCount != 1 {
		t.Fatalf("expected exactly one batch approval ask, got %#v", stream.pending)
	}
	if len(stream.messages) != 4 {
		t.Fatalf("expected mixed batch to end with single user summary, got %#v", stream.messages)
	}
	for index := 0; index < 3; index++ {
		if stream.messages[index].Role != "tool" {
			t.Fatalf("expected message %d to be tool result, got %#v", index, stream.messages[index])
		}
	}
	summary := stream.messages[3]
	if summary.Role != "user" {
		t.Fatalf("expected final mixed-batch message to be user summary, got %#v", summary)
	}
	text, _ := summary.Content.(string)
	if !strings.Contains(text, `[HITL] 审批结果：`) ||
		!strings.Contains(text, `1. chmod 777 ~/a.sh → approve`) ||
		!strings.Contains(text, `2. chmod 777 ~/b.sh → approve`) ||
		!strings.Contains(text, `3. chmod 777 ~/c.sh → reject（风险过高）`) {
		t.Fatalf("unexpected mixed summary %#v", summary)
	}
}

func TestPrepareQueuedBashApprovalBatch_LeavesHtmlViewportOutsideMergedApprovalAsk(t *testing.T) {
	executor := &recordingToolExecutor{
		defs: []api.ToolDetailResponse{
			bashToolDefinition(),
			backendToolDefinition("weather_tool"),
		},
		result: contracts.ToolExecutionResult{
			Output:   "ok",
			ExitCode: 0,
		},
	}
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
		runControl: contracts.NewRunControl(context.Background(), "run_1"),
		execCtx: &contracts.ExecutionContext{
			Budget: contracts.Budget{Tool: contracts.RetryPolicy{TimeoutMs: 50}},
		},
		checker: commandResultChecker{
			results: map[string]hitl.InterceptResult{
				"chmod 777 ~/a.sh": {
					Intercepted:     true,
					Rule:            hitl.FlatRule{Level: 1, ViewportType: "builtin", ViewportKey: "confirm_dialog"},
					OriginalCommand: "chmod 777 ~/a.sh",
				},
				sampleLeaveCommand(3): {
					Intercepted:     true,
					Rule:            hitl.FlatRule{Level: 1, ViewportType: "html", ViewportKey: "leave_form"},
					OriginalCommand: sampleLeaveCommand(3),
					ParsedCommand: hitl.CommandComponents{
						BaseCommand: "mock",
						Tokens:      []string{"create-leave", "--payload", `{"applicant_id":"E1001","days":3,"department_id":"engineering","end_date":"2026-04-22","leave_type":"annual","reason":"family_trip","start_date":"2026-04-20"}`},
					},
				},
			},
			tools: map[string]api.ToolDetailResponse{
				strings.ToLower(bashToolDefinition().Name): bashToolDefinition(),
			},
		},
		queuedToolCalls: []*preparedToolInvocation{
			{toolID: "tool_1", toolName: "_bash_", args: map[string]any{"command": "chmod 777 ~/a.sh", "description": "放开 a.sh 权限"}},
			{toolID: "tool_2", toolName: "_bash_", args: map[string]any{"command": sampleLeaveCommand(3), "description": "创建请假单"}},
		},
	}

	if !stream.prepareQueuedBashApprovalBatch() {
		t.Fatal("expected merged approval batch")
	}
	ask, ok := stream.pending[0].(contracts.DeltaAwaitAsk)
	if !ok {
		t.Fatalf("expected first pending delta to be await ask, got %#v", stream.pending)
	}
	if len(ask.Approvals) != 1 {
		t.Fatalf("expected only builtin approval in merged ask, got %#v", ask)
	}

	ack := stream.runControl.ResolveSubmit(api.SubmitRequest{
		RunID:      "run_1",
		AwaitingID: ask.AwaitingID,
		Params: encodedSubmitParams(t, []map[string]any{
			{"id": "tool_1", "decision": "approve"},
		}),
	})
	if !ack.Accepted {
		t.Fatalf("expected submit to be accepted, got %#v", ack)
	}
	if err := stream.awaitHITLApprovalBatchAndContinue(); err != nil {
		t.Fatalf("awaitHITLApprovalBatchAndContinue returned error: %v", err)
	}

	stream.activateNextToolCall()
	if err := stream.invokeActiveToolCall(); err != nil {
		t.Fatalf("invokeActiveToolCall returned error: %v", err)
	}
	stream.activateNextToolCall()
	if err := stream.invokeActiveToolCall(); err != nil {
		t.Fatalf("invokeActiveToolCall returned error: %v", err)
	}

	foundFormAsk := false
	for _, delta := range stream.pending {
		if typed, ok := delta.(contracts.DeltaAwaitAsk); ok && typed.Mode == "form" && typed.ViewportKey == "leave_form" {
			foundFormAsk = true
		}
	}
	if !foundFormAsk {
		t.Fatalf("expected html invocation to emit its own form await, got %#v", stream.pending)
	}
}

func TestAppendOriginalToolResult_DoesNotAppendHITLSummaryWithoutApprovalEntries(t *testing.T) {
	stream := &llmRunStream{
		engine: &LLMAgentEngine{
			tools: stubToolExecutor{defs: []api.ToolDetailResponse{bashToolDefinition()}},
		},
		execCtx: &contracts.ExecutionContext{},
	}
	invocation := &preparedToolInvocation{
		toolID:   "tool_1",
		toolName: "_bash_",
		args:     map[string]any{"command": "ls"},
	}
	stream.appendOriginalToolResult(invocation, contracts.ToolExecutionResult{
		Output:   "ok",
		ExitCode: 0,
	})
	if len(stream.messages) != 1 || stream.messages[0].Role != "tool" {
		t.Fatalf("expected only tool message without HITL summary, got %#v", stream.messages)
	}
}

func TestPrepareQueuedBashApprovalBatch_SkipsWhitelistedRuleWithinRun(t *testing.T) {
	stream := &llmRunStream{
		session: contracts.QuerySession{RunID: "run_1"},
		hitlRuleWhitelist: map[string]struct{}{
			"dangerous::chmod::777::1::builtin::confirm_dialog": {},
		},
		checker: commandResultChecker{
			results: map[string]hitl.InterceptResult{
				"chmod 777 ~/d.sh": {
					Intercepted:     true,
					Rule:            hitl.FlatRule{RuleKey: "dangerous::chmod::777::1::builtin::confirm_dialog", Level: 1, ViewportType: "builtin", ViewportKey: "confirm_dialog"},
					OriginalCommand: "chmod 777 ~/d.sh",
				},
			},
		},
		queuedToolCalls: []*preparedToolInvocation{
			{toolID: "tool_4", toolName: "_bash_", args: map[string]any{"command": "chmod 777 ~/d.sh", "description": "放开 d.sh 权限"}},
		},
	}

	if stream.prepareQueuedBashApprovalBatch() {
		t.Fatalf("expected whitelisted rule to skip approval batch, got %#v", stream.pending)
	}
	if decision := stream.queuedToolCalls[0].approvalDecision; decision != "approve_prefix_run" {
		t.Fatalf("expected invocation to inherit approve_prefix_run, got %#v", stream.queuedToolCalls[0])
	}
	if stream.queuedToolCalls[0].hitlDecision == nil || stream.queuedToolCalls[0].hitlDecision.Scope != "run_rule" {
		t.Fatalf("expected whitelisted invocation to record run_rule HITL metadata, got %#v", stream.queuedToolCalls[0])
	}
	if stream.queuedToolCalls[0].hitlDecision.Mode != "approval" {
		t.Fatalf("expected whitelisted invocation to preserve approval mode, got %#v", stream.queuedToolCalls[0].hitlDecision)
	}
}

func TestPrepareQueuedBashApprovalBatch_BlocksEntireTurnAndResumesInOriginalOrder(t *testing.T) {
	executor := &recordingToolExecutor{
		defs: []api.ToolDetailResponse{
			bashToolDefinition(),
			backendToolDefinition("weather_tool"),
		},
		result: contracts.ToolExecutionResult{
			Output:   "ok",
			ExitCode: 0,
		},
	}
	stream := &llmRunStream{
		ctx: context.Background(),
		engine: &LLMAgentEngine{
			tools: executor,
		},
		session: contracts.QuerySession{
			RequestID: "req_1",
			ChatID:    "chat_1",
			RunID:     "run_1",
		},
		runControl: contracts.NewRunControl(context.Background(), "run_1"),
		execCtx: &contracts.ExecutionContext{
			Budget: contracts.Budget{Tool: contracts.RetryPolicy{TimeoutMs: 50}},
		},
		checker: commandResultChecker{
			results: map[string]hitl.InterceptResult{
				"chmod 777 ~/a.sh": {
					Intercepted:     true,
					Rule:            hitl.FlatRule{Level: 1, ViewportType: "builtin", ViewportKey: "confirm_dialog"},
					OriginalCommand: "chmod 777 ~/a.sh",
				},
				"chmod 777 ~/b.sh": {
					Intercepted:     true,
					Rule:            hitl.FlatRule{Level: 1, ViewportType: "builtin", ViewportKey: "confirm_dialog"},
					OriginalCommand: "chmod 777 ~/b.sh",
				},
			},
			tools: map[string]api.ToolDetailResponse{
				strings.ToLower(bashToolDefinition().Name):                  bashToolDefinition(),
				strings.ToLower(backendToolDefinition("weather_tool").Name): backendToolDefinition("weather_tool"),
			},
		},
		queuedToolCalls: []*preparedToolInvocation{
			{toolID: "tool_1", toolName: "_bash_", args: map[string]any{"command": "chmod 777 ~/a.sh", "description": "放开 a.sh 权限"}},
			{toolID: "tool_2", toolName: "weather_tool", args: map[string]any{"city": "Shanghai"}},
			{toolID: "tool_3", toolName: "_bash_", args: map[string]any{"command": "chmod 777 ~/b.sh", "description": "放开 b.sh 权限"}},
		},
	}

	if !stream.prepareQueuedBashApprovalBatch() {
		t.Fatal("expected batch approval await to be prepared")
	}
	if len(executor.invocations) != 0 {
		t.Fatalf("expected no tool execution before submit, got %#v", executor.invocations)
	}

	ask, ok := stream.pending[0].(contracts.DeltaAwaitAsk)
	if !ok {
		t.Fatalf("expected first pending delta to be await ask, got %#v", stream.pending)
	}
	if len(ask.Approvals) != 2 {
		t.Fatalf("expected two blocked approvals in merged ask, got %#v", ask)
	}

	ack := stream.runControl.ResolveSubmit(api.SubmitRequest{
		RunID:      "run_1",
		AwaitingID: ask.AwaitingID,
		Params: encodedSubmitParams(t, []map[string]any{
			{"id": "tool_1", "decision": "approve"},
			{"id": "tool_3", "decision": "reject"},
		}),
	})
	if !ack.Accepted {
		t.Fatalf("expected submit to be accepted, got %#v", ack)
	}
	if err := stream.awaitHITLApprovalBatchAndContinue(); err != nil {
		t.Fatalf("awaitHITLApprovalBatchAndContinue returned error: %v", err)
	}

	for len(stream.queuedToolCalls) > 0 {
		stream.activateNextToolCall()
		if err := stream.invokeActiveToolCall(); err != nil {
			t.Fatalf("invokeActiveToolCall returned error: %v", err)
		}
	}

	if len(executor.invocations) != 2 {
		t.Fatalf("expected approved bash plus unblocked tool to execute, got %#v", executor.invocations)
	}
	if executor.invocations[0].name != "_bash_" || executor.invocations[0].args["command"] != "chmod 777 ~/a.sh" {
		t.Fatalf("expected first execution to be approved first bash, got %#v", executor.invocations)
	}
	if executor.invocations[1].name != "weather_tool" || executor.invocations[1].args["city"] != "Shanghai" {
		t.Fatalf("expected unblocked tool to resume in original order after submit, got %#v", executor.invocations)
	}

	approvalAskCount := 0
	rejectedCount := 0
	for _, delta := range stream.pending {
		switch typed := delta.(type) {
		case contracts.DeltaAwaitAsk:
			if typed.AwaitingID == ask.AwaitingID && typed.Mode == "approval" {
				approvalAskCount++
			}
		case contracts.DeltaToolResult:
			if typed.ToolID == "tool_3" && typed.Result.Error == "user_rejected" {
				rejectedCount++
			}
		}
	}
	if approvalAskCount != 1 {
		t.Fatalf("expected exactly one approval ask for the whole turn, got %#v", stream.pending)
	}
	if rejectedCount != 1 {
		t.Fatalf("expected rejected blocked command to emit user_rejected result, got %#v", stream.pending)
	}
}

func TestAwaitHITLSubmitAndExecute_TimeoutEmitsTerminalAnswer(t *testing.T) {
	stream := &llmRunStream{
		ctx: context.Background(),
		engine: &LLMAgentEngine{
			tools:    &recordingToolExecutor{defs: []api.ToolDetailResponse{bashToolDefinition()}},
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
			toolName: "_bash_",
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
	stream.runControl.ExpectSubmit(contracts.AwaitingSubmitContext{
		AwaitingID: stream.hitlAwaitingID,
		Mode:       "approval",
	})

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
				errPayload, _ := typed.Answer["error"].(map[string]any)
				if typed.Answer["status"] != "error" || errPayload["code"] != "timeout" {
					t.Fatalf("unexpected timeout answer %#v", typed.Answer)
				}
			}
		case contracts.DeltaToolResult:
			if typed.ToolName == "_bash_" {
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

func TestAwaitHITLSubmitAndExecute_FormTimeoutEmitsHITLMetadataAndSummary(t *testing.T) {
	stream := &llmRunStream{
		ctx: context.Background(),
		engine: &LLMAgentEngine{
			tools:    &recordingToolExecutor{defs: []api.ToolDetailResponse{bashToolDefinition()}},
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
			toolName: "_bash_",
			args: map[string]any{
				"command": sampleLeaveCommand(3),
			},
		},
		hitlMatch: &hitl.InterceptResult{
			Intercepted: true,
			Rule: hitl.FlatRule{
				Match:        "create-leave",
				Level:        1,
				ViewportType: "html",
				ViewportKey:  "leave_form",
			},
		},
		hitlAwaitingID: buildHITLAwaitingID("tool_1"),
		hitlAwaitArgs: map[string]any{
			"mode": "form",
		},
	}
	stream.runControl.ExpectSubmit(contracts.AwaitingSubmitContext{
		AwaitingID: stream.hitlAwaitingID,
		Mode:       "form",
	})

	if err := stream.awaitHITLSubmitAndExecute(); err != nil {
		t.Fatalf("awaitHITLSubmitAndExecute returned error: %v", err)
	}

	foundResult := false
	for _, delta := range stream.pending {
		typed, ok := delta.(contracts.DeltaToolResult)
		if !ok || typed.ToolName != "_bash_" {
			continue
		}
		foundResult = true
		if typed.Result.Error != "hitl_timeout" {
			t.Fatalf("expected hitl_timeout tool result, got %#v", typed.Result)
		}
		if typed.Result.HITL["mode"] != "form" || typed.Result.HITL["decision"] != "reject" || typed.Result.HITL["reason"] != "timeout" {
			t.Fatalf("expected form timeout HITL metadata, got %#v", typed.Result.HITL)
		}
		if _, ok := typed.Result.HITL["submittedPayload"]; ok {
			t.Fatalf("did not expect submitted payload for form timeout, got %#v", typed.Result.HITL)
		}
	}
	if !foundResult {
		t.Fatalf("expected timeout tool result, got %#v", stream.pending)
	}
	if len(stream.messages) < 2 {
		t.Fatalf("expected timeout tool message and HITL summary, got %#v", stream.messages)
	}
	noticeText, _ := stream.messages[len(stream.messages)-1].Content.(string)
	if !strings.Contains(noticeText, ` → reject（timeout）`) || strings.Contains(noticeText, "提交参数:") {
		t.Fatalf("expected form timeout HITL summary without submitted payload, got %#v", stream.messages[len(stream.messages)-1])
	}
}

func TestInvokeActiveToolCallUsesSkillScopedChecker(t *testing.T) {
	executor := &recordingToolExecutor{
		defs: []api.ToolDetailResponse{bashToolDefinition()},
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
			toolName: "_bash_",
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
			command:  sampleLeaveCommand(3),
			expected: sampleLeavePayload(3),
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
	args := stream.buildFormApprovalArgs(`mock create-leave --payload {"applicant_id":"E1001","department_id":"engineering","leave_type":"annual","start_date":"2026-04-20","end_date":"2026-04-22","days":3,"reason":"family_trip"}`, hitl.InterceptResult{
		Rule: hitl.FlatRule{
			ViewportType: "html",
			ViewportKey:  "leave_form",
			Title:        "mock 请假申请",
		},
		ParsedCommand: hitl.CommandComponents{
			BaseCommand: "mock",
			Tokens:      []string{"create-leave", "--payload", "{applicant_id:E1001}"},
		},
		OriginalCommand: `mock create-leave --payload {"applicant_id":"E1001","department_id":"engineering","leave_type":"annual","start_date":"2026-04-20","end_date":"2026-04-22","days":3,"reason":"family_trip"}`,
	})
	forms, ok := args["forms"].([]any)
	if !ok || len(forms) != 1 {
		t.Fatalf("expected forms in form approval args, got %#v", args)
	}
	form := forms[0].(map[string]any)
	payload, ok := form["payload"].(map[string]any)
	if !ok {
		t.Fatalf("expected payload in form approval args, got %#v", args)
	}
	expected := sampleLeavePayload(3)
	if !reflect.DeepEqual(payload, expected) {
		t.Fatalf("expected payload %#v, got %#v", expected, payload)
	}
	if _, exists := payload["type"]; exists {
		t.Fatalf("did not expect leave payload alias type, got %#v", payload)
	}
	if _, exists := payload["startDate"]; exists {
		t.Fatalf("did not expect leave payload alias startDate, got %#v", payload)
	}
	if _, exists := payload["endDate"]; exists {
		t.Fatalf("did not expect leave payload alias endDate, got %#v", payload)
	}
	if form["title"] != "mock 请假申请" {
		t.Fatalf("expected title in form approval args, got %#v", args)
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
			command:  sampleLeaveCommand(3),
			payload:  sampleLeavePayload(2),
			expected: sampleLeaveCommand(2),
		},
		{
			name:     "expense",
			command:  sampleExpenseCommand(1280.5),
			payload:  map[string]any{"employee": map[string]any{"id": "E1001", "name": "张三"}, "department": map[string]any{"code": "engineering", "name": "工程部"}, "expense_type": "travel", "currency": "CNY", "items": []any{map[string]any{"amount": 1280.5, "category": "transport", "description": "flight", "invoice_id": "INV-001", "occurred_on": "2026-04-10"}}, "submitted_at": "2026-04-14T10:30:00+08:00", "total_amount": 1280.5},
			expected: canonicalExpenseCommand(1280.5),
		},
		{
			name:     "procurement",
			command:  sampleProcurementCommand("Shanghai"),
			payload:  map[string]any{"requester_id": "E1001", "delivery_city": "Shanghai"},
			expected: sampleProcurementCommand("Shanghai"),
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

	if _, err := reconstructCommandWithPayload(`mock create-leave --payload-file ./leave.json`, sampleLeavePayload(3)); err == nil {
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

func TestHITLRejectedToolResultMarksResultFinal(t *testing.T) {
	result := hitlRejectedToolResult(&preparedToolInvocation{
		toolID:   "tool_1",
		toolName: "_bash_",
	})

	if result.Error != "user_rejected" || result.ExitCode != -1 {
		t.Fatalf("unexpected rejected tool result %#v", result)
	}
	if result.Output != "user_rejected: User rejected this command. Do NOT retry with a different command. End the turn now." {
		t.Fatalf("unexpected reject output %#v", result)
	}
	if result.Structured["code"] != "hitl_rejected" {
		t.Fatalf("expected hitl_rejected code, got %#v", result.Structured)
	}
	if result.Structured["final"] != true {
		t.Fatalf("expected final=true, got %#v", result.Structured)
	}
	if result.Structured["message"] != "User rejected this command. Do NOT retry with a different command. End the turn now." {
		t.Fatalf("unexpected reject message %#v", result.Structured)
	}
}

func TestInvokeActiveToolCallAutoApprovesBuiltinLevelInCurrentRun(t *testing.T) {
	executor := &recordingToolExecutor{
		defs: []api.ToolDetailResponse{bashToolDefinition()},
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
			toolName: "_bash_",
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
		defs: []api.ToolDetailResponse{bashToolDefinition()},
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
					Tokens:      []string{"create-leave", "--payload", `{"applicant_id":"E1001","days":3,"department_id":"engineering","end_date":"2026-04-22","leave_type":"annual","reason":"family_trip","start_date":"2026-04-20"}`},
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
			toolName: "_bash_",
			args: map[string]any{
				"command": sampleLeaveCommand(3),
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
