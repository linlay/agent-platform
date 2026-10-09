package server

import (
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"agent-platform/internal/runtime/query"
)

func TestValidateDeferredSubmitParamsAcceptsDismissAndValidShapes(t *testing.T) {
	lists := []struct {
		name   string
		mode   string
		params any
	}{
		{name: "question dismiss", mode: "question", params: []map[string]any{}},
		{name: "question answer", mode: "question", params: []map[string]any{{"answer": "Approve"}}},
		{name: "approval decision", mode: "approval", params: []map[string]any{{"decision": "approve"}}},
		{name: "approval rule decision", mode: "approval", params: []map[string]any{{"decision": "approve_rule_run"}}},
	}
	for _, tt := range lists {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateDeferredSubmitParams(tt.mode, mustEncodeSubmitParams(t, tt.params)); err != nil {
				t.Fatalf("validateDeferredSubmitParams returned error: %v", err)
			}
		})
	}
	singles := []struct {
		name  string
		mode  string
		param api.SubmitParam
	}{
		{name: "form approve", mode: "form", param: api.SubmitParam{"decision": "approve", "data": map[string]any{"days": 2}}},
		{name: "form reject", mode: "form", param: api.SubmitParam{"decision": "reject"}},
		{name: "form reject with reason", mode: "form", param: api.SubmitParam{"decision": "reject", "reason": "不同意"}},
		{name: "form reject with data", mode: "form", param: api.SubmitParam{"decision": "reject", "reason": "已修改", "data": map[string]any{"days": 1}}},
		{name: "form dismiss", mode: "form", param: api.SubmitParam{"decision": "dismiss"}},
		{name: "planning dismiss", mode: "planning", param: api.SubmitParam{"decision": "dismiss"}},
		{name: "planning approve", mode: "planning", param: api.SubmitParam{"decision": "approve"}},
		{name: "planning reject empty reason", mode: "planning", param: api.SubmitParam{"decision": "reject", "reason": ""}},
		{name: "planning reject reason", mode: "planning", param: api.SubmitParam{"decision": "reject", "reason": "请补充测试范围"}},
	}
	for _, tt := range singles {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateDeferredSubmitParam(tt.mode, tt.param); err != nil {
				t.Fatalf("validateDeferredSubmitParam returned error: %v", err)
			}
		})
	}
}

// Each awaiting mode accepts exactly one of param and params; a wrong or
// ambiguous field must be rejected instead of being read as a dismissal.
func TestValidateSubmitRejectsWrongAnswerField(t *testing.T) {
	approve := api.SubmitParam{"decision": "approve", "data": map[string]any{}}
	list := mustEncodeSubmitParams(t, []map[string]any{{"decision": "approve"}})
	tests := []struct {
		name       string
		mode       string
		request    api.SubmitRequest
		wantSubstr string
	}{
		{name: "form with params", mode: "form", request: api.SubmitRequest{Params: list}, wantSubstr: "form awaiting accepts param, not params"},
		{name: "form with empty params", mode: "form", request: api.SubmitRequest{Params: api.SubmitParams{}}, wantSubstr: "form awaiting accepts param, not params"},
		{name: "form with both", mode: "form", request: api.SubmitRequest{Param: approve, Params: list}, wantSubstr: "form awaiting accepts param, not params"},
		{name: "form without answer", mode: "form", request: api.SubmitRequest{}, wantSubstr: "form awaiting requires a non-empty param object"},
		{name: "planning with params", mode: "planning", request: api.SubmitRequest{Params: list}, wantSubstr: "planning awaiting accepts param, not params"},
		{name: "planning without answer", mode: "planning", request: api.SubmitRequest{}, wantSubstr: "planning awaiting requires a non-empty param object"},
		{name: "question with param", mode: "question", request: api.SubmitRequest{Param: api.SubmitParam{"answer": "x"}}, wantSubstr: "question awaiting accepts params, not param"},
		{name: "approval with param", mode: "approval", request: api.SubmitRequest{Param: api.SubmitParam{"decision": "approve"}}, wantSubstr: "approval awaiting accepts params, not param"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := query.ValidateSubmitParams(contracts.AwaitingSubmitContext{AwaitingID: "await_1", Mode: tt.mode, ItemCount: 1}, tt.request)
			if err == nil || !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateDeferredSubmitParamsRejectsInvalidApprovalDecision(t *testing.T) {
	invalidDecision := "approve_" + "prefix_run"
	err := validateDeferredSubmitParams("approval", mustEncodeSubmitParams(t, []map[string]any{{"decision": invalidDecision}}))
	if err == nil || !strings.Contains(err.Error(), `items[0]: unsupported approval decision "`+invalidDecision+`"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateDeferredSubmitParamsRejectsInvalidPlanningShape(t *testing.T) {
	tests := []struct {
		name       string
		param      api.SubmitParam
		wantSubstr string
	}{
		{name: "missing decision", param: api.SubmitParam{"reason": "x"}, wantSubstr: "param.decision is required"},
		{name: "invalid decision", param: api.SubmitParam{"decision": "approve_rule_run"}, wantSubstr: `param: unsupported planning decision "approve_rule_run"`},
		{name: "answer rejected", param: api.SubmitParam{"decision": "reject", "answer": "no"}, wantSubstr: "param: planning awaiting does not allow answer"},
		{name: "payload rejected", param: api.SubmitParam{"decision": "reject", "payload": map[string]any{}}, wantSubstr: "param: planning awaiting does not allow payload"},
		{name: "data rejected", param: api.SubmitParam{"decision": "approve", "data": map[string]any{}}, wantSubstr: "param: planning awaiting does not allow data"},
		{name: "id rejected", param: api.SubmitParam{"id": "confirm", "decision": "approve"}, wantSubstr: "param: planning awaiting does not allow id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDeferredSubmitParam("planning", tt.param)
			if err == nil || !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateDeferredSubmitParamsRejectsLegacyPlanMode(t *testing.T) {
	err := validateDeferredSubmitParams("plan", mustEncodeSubmitParams(t, []map[string]any{{"decision": "approve"}}))
	if err == nil || !strings.Contains(err.Error(), "unsupported awaiting mode: plan") {
		t.Fatalf("expected legacy plan mode rejection, got %v", err)
	}
}

func TestValidateDeferredSubmitParamsRejectsInvalidFormShape(t *testing.T) {
	tests := []struct {
		name       string
		param      api.SubmitParam
		wantSubstr string
	}{
		{name: "missing decision", param: api.SubmitParam{"data": map[string]any{"days": 2}}, wantSubstr: "param.decision is required"},
		{name: "invalid decision", param: api.SubmitParam{"decision": "cancel", "data": map[string]any{"days": 2}}, wantSubstr: `param: unsupported form decision "cancel"`},
		{name: "approve missing data", param: api.SubmitParam{"decision": "approve"}, wantSubstr: "param.data is required for approve"},
		{name: "data not object", param: api.SubmitParam{"decision": "approve", "data": "bad"}, wantSubstr: "param.data must be an object"},
		{name: "dismiss with data", param: api.SubmitParam{"decision": "dismiss", "data": map[string]any{}}, wantSubstr: "param: dismiss does not allow data"},
		{name: "old form field rejected", param: api.SubmitParam{"decision": "approve", "form": map[string]any{"days": 2}}, wantSubstr: "param: form awaiting does not allow form"},
		{name: "id rejected", param: api.SubmitParam{"id": "form-1", "decision": "reject"}, wantSubstr: "param: form awaiting does not allow id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDeferredSubmitParam("form", tt.param)
			if err == nil || !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateSubmitParamsValidatesQuestionDefinitions(t *testing.T) {
	questions := []any{
		map[string]any{
			"id":       "q1",
			"question": "生活习惯",
			"type":     "multi-select",
			"options": []any{
				map[string]any{"label": "早睡"},
				map[string]any{"label": "运动"},
			},
		},
		map[string]any{
			"id":       "q2",
			"question": "每周运动次数",
			"type":     "number",
		},
	}
	context := contracts.AwaitingSubmitContext{
		AwaitingID: "await_1",
		Mode:       "question",
		ItemCount:  len(questions),
		Questions:  questions,
	}
	singleSelectContext := contracts.AwaitingSubmitContext{
		AwaitingID: "await_select",
		Mode:       "question",
		ItemCount:  1,
		Questions: []any{map[string]any{
			"id":       "q1",
			"question": "通勤方式",
			"type":     "select",
			"options":  []any{map[string]any{"label": "步行"}},
		}},
	}
	freeTextContext := contracts.AwaitingSubmitContext{
		AwaitingID: "await_free_text",
		Mode:       "question",
		ItemCount:  1,
		Questions: []any{map[string]any{
			"id":            "q1",
			"question":      "其他习惯",
			"type":          "multi-select",
			"allowFreeText": true,
			"options":       []any{map[string]any{"label": "早睡"}},
		}},
	}

	tests := []struct {
		name       string
		context    contracts.AwaitingSubmitContext
		params     any
		wantSubstr string
	}{
		{
			name: "valid multi-select and number",
			params: []map[string]any{
				{"id": "wrong-id", "answers": []string{"早睡", "运动"}},
				{"answer": 3},
			},
		},
		{
			name: "multi-select rejects answer",
			params: []map[string]any{
				{"answer": "早睡"},
				{"answer": 3},
			},
			wantSubstr: "生活习惯: answers is required for multi-select questions",
		},
		{
			name:    "single-select rejects answers",
			context: singleSelectContext,
			params: []map[string]any{
				{"answers": []string{"步行"}},
			},
			wantSubstr: "通勤方式: answers is only allowed for multi-select questions",
		},
		{
			name: "multi-select rejects both fields",
			params: []map[string]any{
				{"answer": "早睡", "answers": []string{"早睡"}},
				{"answer": 3},
			},
			wantSubstr: "items[0]: question items require exactly one of answer or answers",
		},
		{
			name: "multi-select rejects invalid option",
			params: []map[string]any{
				{"answers": []string{"熬夜"}},
				{"answer": 3},
			},
			wantSubstr: `生活习惯: answer item "熬夜" is not an allowed option`,
		},
		{
			name: "number rejects string",
			params: []map[string]any{
				{"answers": []string{"早睡"}},
				{"answer": "three"},
			},
			wantSubstr: "每周运动次数: answer must be a number",
		},
		{
			name: "rejects too few answers",
			params: []map[string]any{
				{"answers": []string{"早睡"}},
			},
			wantSubstr: "expected 2 submit items, got 1",
		},
		{
			name: "rejects too many answers",
			params: []map[string]any{
				{"answers": []string{"早睡"}},
				{"answer": 3},
				{"answer": "extra"},
			},
			wantSubstr: "expected 2 submit items, got 3",
		},
		{
			name:    "free text accepts unlisted option",
			context: freeTextContext,
			params: []map[string]any{
				{"answers": []string{"午休"}},
			},
		},
		{
			name:   "batch cancel remains valid",
			params: []map[string]any{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			awaitingContext := context
			if tt.context.AwaitingID != "" {
				awaitingContext = tt.context
			}
			err := validateSubmitParams(awaitingContext, mustEncodeSubmitParams(t, tt.params))
			if tt.wantSubstr == "" {
				if err != nil {
					t.Fatalf("validateSubmitParams returned error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
