package llm

import (
	"context"
	"reflect"
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

func TestPreToolInvocationDeltas_ApprovalUsesFrontendHandlerAwaitAsk(t *testing.T) {
	tool := api.ToolDetailResponse{
		Name: "_ask_user_approval_",
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
		execCtx: &contracts.ExecutionContext{
			Budget: contracts.Budget{Tool: contracts.RetryPolicy{TimeoutMs: 50}},
		},
		runControl: contracts.NewRunControl(context.Background(), "run_1"),
	}

	deltas := stream.preToolInvocationDeltas("tool_1", "_ask_user_approval_", map[string]any{
		"mode":     "approval",
		"question": "Need confirmation",
		"options": []any{
			map[string]any{"label": "Approve", "value": "approve"},
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

func TestExtractMockLeavePayload(t *testing.T) {
	payload := extractMockLeavePayload(hitl.ParseCommandComponents(`mock create-leave --payload '{"employee_id":"E1001","days":3}'`))
	if payload["employee_id"] != "E1001" {
		t.Fatalf("expected employee_id to be parsed, got %#v", payload)
	}
	if payload["days"] != float64(3) {
		t.Fatalf("expected days to be parsed, got %#v", payload)
	}

	if got := extractMockLeavePayload(hitl.ParseCommandComponents(`mock create-leave --payload-file ./leave.json`)); got != nil {
		t.Fatalf("expected payload-file command to skip prefill, got %#v", got)
	}
}
