package server

import (
	"strings"
	"testing"

	"agent-platform/internal/contracts"
)

func TestValidateDeferredSubmitParamsAcceptsDismissAndValidShapes(t *testing.T) {
	tests := []struct {
		name   string
		mode   string
		params any
	}{
		{name: "question dismiss", mode: "question", params: []map[string]any{}},
		{name: "question answer", mode: "question", params: []map[string]any{{"answer": "Approve"}}},
		{name: "approval decision", mode: "approval", params: []map[string]any{{"decision": "approve"}}},
		{name: "approval rule decision", mode: "approval", params: []map[string]any{{"decision": "approve_rule_run"}}},
		{name: "form approve", mode: "form", params: []map[string]any{{"decision": "approve", "form": map[string]any{"days": 2}}}},
		{name: "form reject", mode: "form", params: []map[string]any{{"decision": "reject"}}},
		{name: "form reject with reason", mode: "form", params: []map[string]any{{"decision": "reject", "reason": "不同意"}}},
		{name: "form reject with form", mode: "form", params: []map[string]any{{"decision": "reject", "reason": "已修改", "form": map[string]any{"days": 1}}}},
		{name: "planning dismiss", mode: "planning", params: []map[string]any{}},
		{name: "planning approve", mode: "planning", params: []map[string]any{{"decision": "approve"}}},
		{name: "planning reject empty reason", mode: "planning", params: []map[string]any{{"decision": "reject", "reason": ""}}},
		{name: "planning reject reason", mode: "planning", params: []map[string]any{{"decision": "reject", "reason": "请补充测试范围"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDeferredSubmitParams(tt.mode, mustEncodeSubmitParams(t, tt.params))
			if err != nil {
				t.Fatalf("validateDeferredSubmitParams returned error: %v", err)
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
		params     any
		wantSubstr string
	}{
		{
			name:       "too many items",
			params:     []map[string]any{{"decision": "approve"}, {"decision": "reject"}},
			wantSubstr: "expected 1 submit items, got 2",
		},
		{
			name:       "invalid decision",
			params:     []map[string]any{{"decision": "approve_rule_run"}},
			wantSubstr: `items[0]: unsupported planning decision "approve_rule_run"`,
		},
		{
			name:       "answer rejected",
			params:     []map[string]any{{"decision": "reject", "answer": "no"}},
			wantSubstr: "items[0]: planning items do not allow answer",
		},
		{
			name:       "payload rejected",
			params:     []map[string]any{{"decision": "reject", "payload": map[string]any{}}},
			wantSubstr: "items[0]: planning items do not allow payload",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDeferredSubmitParams("planning", mustEncodeSubmitParams(t, tt.params))
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

func TestValidateDeferredSubmitParamsRejectsInvalidShape(t *testing.T) {
	tests := []struct {
		name       string
		params     any
		wantSubstr string
	}{
		{
			name:       "missing decision",
			params:     []map[string]any{{"form": map[string]any{"days": 2}}},
			wantSubstr: "items[0]: form items require decision",
		},
		{
			name:       "invalid decision",
			params:     []map[string]any{{"decision": "cancel", "form": map[string]any{"days": 2}}},
			wantSubstr: `items[0]: unsupported form decision "cancel"`,
		},
		{
			name:       "approve missing form",
			params:     []map[string]any{{"decision": "approve"}},
			wantSubstr: "items[0]: approve decision requires form",
		},
		{
			name:       "form not object",
			params:     []map[string]any{{"decision": "approve", "form": "bad"}},
			wantSubstr: "items[0]: form field must be an object",
		},
		{
			name:       "action field rejected",
			params:     []map[string]any{{"action": "submit", "form": map[string]any{"days": 2}}},
			wantSubstr: "items[0]: form items no longer use action, use decision instead",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDeferredSubmitParams("form", mustEncodeSubmitParams(t, tt.params))
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

func TestValidateTeamMergedSubmitParamsUsesReversibleFieldRoutes(t *testing.T) {
	ctx := contracts.AwaitingSubmitContext{
		AwaitingID: "run_1_team_await_1",
		Mode:       "form",
		ItemCount:  2,
		Routes: []contracts.AwaitingSubmitRoute{
			{
				FieldID: "run_1_team_t_1:raw_await", TaskID: "run_1_team_t_1", AwaitingID: "raw_await",
				Mode: "question", ItemCount: 1,
				Questions: []any{map[string]any{"id": "q1", "question": "Pick", "type": "select", "options": []any{map[string]any{"label": "yes"}}}},
			},
			{
				FieldID: "run_1_team_t_2:raw_await", TaskID: "run_1_team_t_2", AwaitingID: "raw_await",
				Mode: "approval", ItemCount: 1,
			},
		},
	}
	valid := []map[string]any{
		{
			"id": "run_1_team_t_1:raw_await", "decision": "approve",
			"form": map[string]any{"params": []any{map[string]any{"answer": "yes"}}},
		},
		{
			"id": "run_1_team_t_2:raw_await", "decision": "approve",
			"form": map[string]any{"params": []any{map[string]any{"decision": "approve"}}},
		},
	}
	if err := validateSubmitParams(ctx, mustEncodeSubmitParams(t, valid)); err != nil {
		t.Fatalf("valid merged submit rejected: %v", err)
	}

	wrongID := append([]map[string]any(nil), valid...)
	wrongID[0] = map[string]any{
		"id": "raw_await", "decision": "approve",
		"form": map[string]any{"params": []any{map[string]any{"answer": "yes"}}},
	}
	if err := validateSubmitParams(ctx, mustEncodeSubmitParams(t, wrongID)); err == nil || !strings.Contains(err.Error(), "id must be") {
		t.Fatalf("expected reversible field id validation, got %v", err)
	}

	invalidChild := append([]map[string]any(nil), valid...)
	invalidChild[0] = map[string]any{
		"id": "run_1_team_t_1:raw_await", "decision": "approve",
		"form": map[string]any{"params": []any{map[string]any{"answer": "no"}}},
	}
	if err := validateSubmitParams(ctx, mustEncodeSubmitParams(t, invalidChild)); err == nil || !strings.Contains(err.Error(), "answer is not an allowed option") {
		t.Fatalf("expected child question validation, got %v", err)
	}
}
