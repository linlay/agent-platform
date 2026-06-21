package frontendtools

import (
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

func frontendTool(name string) api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Name: name,
		Meta: map[string]any{
			"kind": "frontend",
		},
	}
}

func TestGenericFormHandlerNormalizesPayloadSubmit(t *testing.T) {
	handler := NewGenericFormHandler()
	normalized, err := handler.NormalizeSubmit(map[string]any{"seed": "value"}, []any{
		map[string]any{
			"id":      "form-1",
			"payload": map[string]any{"name": "Lin"},
		},
	})
	if err != nil {
		t.Fatalf("normalize generic submit: %v", err)
	}
	if normalized["mode"] != "form" || normalized["status"] != "answered" {
		t.Fatalf("unexpected normalized payload %#v", normalized)
	}
	forms, ok := normalized["forms"].([]map[string]any)
	if !ok || len(forms) != 1 {
		t.Fatalf("expected one form, got %#v", normalized["forms"])
	}
	if forms[0]["decision"] != "submit" {
		t.Fatalf("expected default submit decision, got %#v", forms[0])
	}
	form, _ := forms[0]["form"].(map[string]any)
	if form["name"] != "Lin" {
		t.Fatalf("unexpected form payload %#v", form)
	}
}

func mustSubmitParams(t *testing.T, value any) api.SubmitParams {
	t.Helper()
	params, err := api.EncodeSubmitParams(value)
	if err != nil {
		t.Fatalf("encode submit params: %v", err)
	}
	return params
}

func TestAskUserQuestionHandlerBuildInitialAwaitAsk(t *testing.T) {
	handler := NewAskUserQuestionHandler()
	awaitAsk := handler.BuildInitialAwaitAsk("tool_1", "run_1", frontendTool("ask_user_question"), map[string]any{
		"mode": "question",
		"questions": []any{
			map[string]any{"question": "Pick a plan", "type": "select", "options": []any{map[string]any{"label": "Weekend"}}},
			map[string]any{"question": "How many people?", "type": "number"},
		},
	}, 0, 5000)
	if awaitAsk == nil {
		t.Fatal("expected initial await ask")
	}
	if awaitAsk.Mode != "question" || awaitAsk.AwaitingID != "tool_1" {
		t.Fatalf("unexpected await ask %#v", awaitAsk)
	}
	if awaitAsk.ViewportType != "builtin" || awaitAsk.ViewportKey != "question" {
		t.Fatalf("expected builtin question viewport metadata, got %#v", awaitAsk)
	}
	questions := awaitAsk.Questions
	if len(questions) != 2 {
		t.Fatalf("expected inline questions, got %#v", awaitAsk.Questions)
	}
	first := questions[0].(map[string]any)
	if first["id"] != "q1" || first["question"] != "Pick a plan" {
		t.Fatalf("unexpected first question %#v", first)
	}
}

func TestAskUserQuestionHandlerValidateArgs(t *testing.T) {
	handler := NewAskUserQuestionHandler()
	if err := handler.ValidateArgs(map[string]any{
		"mode": "question",
		"questions": []any{
			map[string]any{"question": "Pick a plan", "type": "select", "options": []any{map[string]any{"label": "Weekend"}}},
		},
	}); err != nil {
		t.Fatalf("ValidateArgs returned error for valid args: %v", err)
	}

	err := handler.ValidateArgs(map[string]any{
		"mode": "question",
		"questions": []any{
			map[string]any{"question": "Pick a plan", "type": "select"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "options is required for select and multi-select questions") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAskUserQuestionHandlerValidateArgsPreviewHTML(t *testing.T) {
	handler := NewAskUserQuestionHandler()
	validQuestions := []any{
		map[string]any{
			"question": "Pick a plan",
			"type":     "select",
			"options": []any{
				map[string]any{"label": "Plain", "description": "2 days"},
				map[string]any{"label": "Preview", "previewHtml": "<div><strong>Preview</strong></div>"},
				map[string]any{"label": "Both", "description": "Fallback", "previewHtml": "<p>Preview</p>"},
			},
		},
	}
	if err := handler.ValidateArgs(map[string]any{"mode": "question", "questions": validQuestions}); err != nil {
		t.Fatalf("ValidateArgs returned error for previewHtml options: %v", err)
	}

	for _, tc := range []struct {
		name        string
		previewHTML any
	}{
		{name: "empty", previewHTML: ""},
		{name: "blank", previewHTML: "   "},
		{name: "non-string", previewHTML: 42},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := handler.ValidateArgs(map[string]any{
				"mode": "question",
				"questions": []any{
					map[string]any{
						"question": "Pick a plan",
						"type":     "select",
						"options":  []any{map[string]any{"label": "Preview", "previewHtml": tc.previewHTML}},
					},
				},
			})
			if err == nil || !strings.Contains(err.Error(), "previewHtml must be a non-empty string") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestAskUserQuestionHandlerBuildInitialAwaitAskPreservesOptionPreviewHTML(t *testing.T) {
	handler := NewAskUserQuestionHandler()
	awaitAsk := handler.BuildInitialAwaitAsk("tool_1", "run_1", frontendTool("ask_user_question"), map[string]any{
		"mode": "question",
		"questions": []any{
			map[string]any{
				"question": "Pick a plan",
				"type":     "select",
				"options": []any{
					map[string]any{"label": "Weekend", "description": "Fallback", "previewHtml": "<p>Weekend</p>"},
				},
			},
		},
	}, 0, 5000)
	if awaitAsk == nil || len(awaitAsk.Questions) != 1 {
		t.Fatalf("expected await ask with one question, got %#v", awaitAsk)
	}
	question := awaitAsk.Questions[0].(map[string]any)
	options := question["options"].([]any)
	option := options[0].(map[string]any)
	if option["description"] != "Fallback" || option["previewHtml"] != "<p>Weekend</p>" {
		t.Fatalf("expected option previewHtml and description to be preserved, got %#v", option)
	}
}

func TestAskUserQuestionHandlerNormalizeSubmit(t *testing.T) {
	handler := NewAskUserQuestionHandler()
	args := map[string]any{
		"questions": []any{
			map[string]any{
				"question": "Pick a plan",
				"type":     "select",
				"header":   "行程安排",
				"options":  []any{map[string]any{"label": "Weekend"}},
			},
			map[string]any{
				"question": "Notification topics",
				"type":     "multi-select",
				"header":   "通知内容",
				"options": []any{
					map[string]any{"label": "产品更新"},
					map[string]any{"label": "使用教程"},
				},
			},
		},
	}

	result, err := handler.NormalizeSubmit(args, mustSubmitParams(t, []map[string]any{
		{"answer": "Weekend"},
		{"answers": []string{"产品更新", "使用教程"}},
	}))
	if err != nil {
		t.Fatalf("NormalizeSubmit returned error: %v", err)
	}
	answers, ok := result["answers"].([]map[string]any)
	if !ok || len(answers) != 2 {
		t.Fatalf("expected normalized answers, got %#v", result["answers"])
	}
	if result["status"] != "answered" {
		t.Fatalf("expected answered status, got %#v", result)
	}
	if answers[0]["id"] != "q1" || answers[0]["header"] != "行程安排" {
		t.Fatalf("unexpected first answer %#v", answers[0])
	}
	if !reflect.DeepEqual(answers[1]["answer"], []string{"产品更新", "使用教程"}) {
		t.Fatalf("expected multi-select slice, got %#v", answers[1]["answer"])
	}
}

func TestAskUserQuestionHandlerNormalizeSubmitIgnoresSubmittedIDs(t *testing.T) {
	handler := NewAskUserQuestionHandler()
	result, err := handler.NormalizeSubmit(map[string]any{
		"questions": []any{
			map[string]any{"question": "Pick a plan", "type": "select", "options": []any{map[string]any{"label": "Weekend"}}},
			map[string]any{"question": "How many people?", "type": "number"},
		},
	}, mustSubmitParams(t, []map[string]any{
		{"id": "wrong-1", "answer": "Weekend"},
		{"id": "wrong-2", "answer": 2},
	}))
	if err != nil {
		t.Fatalf("NormalizeSubmit returned error: %v", err)
	}
	answers, ok := result["answers"].([]map[string]any)
	if !ok || len(answers) != 2 {
		t.Fatalf("expected normalized answers, got %#v", result["answers"])
	}
	if answers[0]["id"] != "q1" || answers[1]["id"] != "q2" {
		t.Fatalf("expected ids from question definitions, got %#v", answers)
	}
}

func TestAskUserQuestionHandlerNormalizeSubmitDateAndDatetime(t *testing.T) {
	handler := NewAskUserQuestionHandler()
	result, err := handler.NormalizeSubmit(map[string]any{
		"questions": []any{
			map[string]any{"question": "Start date", "type": "date"},
			map[string]any{"question": "Start time", "type": "datetime"},
		},
	}, mustSubmitParams(t, []map[string]any{
		{"answer": "2026-05-15"},
		{"answer": "2026-05-15T09:30"},
	}))
	if err != nil {
		t.Fatalf("NormalizeSubmit returned error: %v", err)
	}
	answers, ok := result["answers"].([]map[string]any)
	if !ok || len(answers) != 2 {
		t.Fatalf("expected normalized answers, got %#v", result["answers"])
	}
	if answers[0]["answer"] != "2026-05-15" || answers[1]["answer"] != "2026-05-15T09:30" {
		t.Fatalf("expected date and datetime strings to pass through, got %#v", answers)
	}
}

func TestAskUserQuestionHandlerRejectsEmptyDateAndDatetime(t *testing.T) {
	handler := NewAskUserQuestionHandler()
	for _, questionType := range []string{"date", "datetime"} {
		_, err := handler.NormalizeSubmit(map[string]any{
			"questions": []any{
				map[string]any{"question": "When?", "type": questionType},
			},
		}, mustSubmitParams(t, []map[string]any{
			{"answer": ""},
		}))
		if err == nil || !strings.Contains(err.Error(), "answer must be a non-empty string") {
			t.Fatalf("expected empty %s answer to be rejected, got %v", questionType, err)
		}
	}
}

func TestAskUserQuestionHandlerRejectsDateAndDatetimeAnswersField(t *testing.T) {
	handler := NewAskUserQuestionHandler()
	for _, questionType := range []string{"date", "datetime"} {
		_, err := handler.NormalizeSubmit(map[string]any{
			"questions": []any{
				map[string]any{"question": "When?", "type": questionType},
			},
		}, mustSubmitParams(t, []map[string]any{
			{"answers": []string{"2026-05-15"}},
		}))
		if err == nil || !strings.Contains(err.Error(), "answers is only allowed for multi-select questions") {
			t.Fatalf("expected answers field for %s to be rejected, got %v", questionType, err)
		}
	}
}

func TestAskUserQuestionHandlerNormalizeSubmitCancel(t *testing.T) {
	handler := NewAskUserQuestionHandler()
	result, err := handler.NormalizeSubmit(map[string]any{
		"questions": []any{map[string]any{"question": "Pick a plan", "type": "select"}},
	}, mustSubmitParams(t, []map[string]any{}))
	if err != nil {
		t.Fatalf("NormalizeSubmit returned error: %v", err)
	}
	expected := map[string]any{
		"mode":   "question",
		"status": "error",
		"error": map[string]any{
			"code":    "user_dismissed",
			"message": "用户关闭等待项",
		},
	}
	if !reflect.DeepEqual(result, expected) {
		t.Fatalf("unexpected cancel result %#v", result)
	}
}

func TestAskUserQuestionHandlerRejectsInvalidAnswerFields(t *testing.T) {
	handler := NewAskUserQuestionHandler()
	_, err := handler.NormalizeSubmit(map[string]any{
		"questions": []any{
			map[string]any{"question": "Notification topics", "type": "multi-select"},
		},
	}, mustSubmitParams(t, []map[string]any{
		{"id": "q1", "answer": "产品更新"},
	}))
	if err == nil || !strings.Contains(err.Error(), "answers is required for multi-select questions") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAskUserQuestionHandlerRejectsAnswerCountMismatch(t *testing.T) {
	handler := NewAskUserQuestionHandler()
	_, err := handler.NormalizeSubmit(map[string]any{
		"questions": []any{
			map[string]any{"question": "Pick a plan", "type": "select", "options": []any{map[string]any{"label": "Weekend"}}},
			map[string]any{"question": "How many people?", "type": "number"},
		},
	}, mustSubmitParams(t, []map[string]any{
		{"answer": "Weekend"},
	}))
	if err == nil || !strings.Contains(err.Error(), "expected 2 answers, got 1") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAskUserQuestionHandlerFormatSubmitResult(t *testing.T) {
	handler := NewAskUserQuestionHandler()
	result := contracts.ToolExecutionResult{
		Output: `{"mode":"question"}`,
		Structured: map[string]any{
			"answers": []any{
				map[string]any{"id": "q1", "question": "Pick a plan", "header": "行程安排", "answer": "Weekend"},
				map[string]any{"id": "q2", "question": "Subscription topics", "header": "订阅内容", "answer": []any{"产品更新", "使用教程"}},
			},
		},
	}
	if got, ok := handler.FormatSubmitResult("summary", result); !ok || got != "用户回答了以下问题:\n- 行程安排: Weekend\n- 订阅内容: 产品更新, 使用教程" {
		t.Fatalf("unexpected summary result: ok=%v got=%q", ok, got)
	}
}

func TestAskUserQuestionHandlerValidateArgsRecommendedSingle(t *testing.T) {
	handler := NewAskUserQuestionHandler()
	err := handler.ValidateArgs(map[string]any{
		"mode": "question",
		"questions": []any{
			map[string]any{
				"question": "Pick a plan",
				"type":     "select",
				"options": []any{
					map[string]any{"label": "A", "recommended": false},
					map[string]any{"label": "B", "recommended": true},
					map[string]any{"label": "C"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("ValidateArgs returned error for single recommended option: %v", err)
	}

	// BuildInitialAwaitAsk should preserve recommended
	awaitAsk := handler.BuildInitialAwaitAsk("tool_1", "run_1", frontendTool("ask_user_question"), map[string]any{
		"mode": "question",
		"questions": []any{
			map[string]any{
				"question": "Pick a plan",
				"type":     "select",
				"options": []any{
					map[string]any{"label": "A", "recommended": false},
					map[string]any{"label": "B", "recommended": true},
					map[string]any{"label": "C"},
				},
			},
		},
	}, 0, 5000)
	if awaitAsk == nil || len(awaitAsk.Questions) != 1 {
		t.Fatalf("expected await ask with one question, got %#v", awaitAsk)
	}
	question := awaitAsk.Questions[0].(map[string]any)
	options := question["options"].([]any)
	if len(options) != 3 {
		t.Fatalf("expected 3 options, got %d", len(options))
	}
	opt0 := options[0].(map[string]any)
	if opt0["recommended"] != false {
		t.Fatalf("expected option 0 recommended=false, got %#v", opt0["recommended"])
	}
	opt1 := options[1].(map[string]any)
	if opt1["recommended"] != true {
		t.Fatalf("expected option 1 recommended=true, got %#v", opt1["recommended"])
	}
	opt2 := options[2].(map[string]any)
	if _, has := opt2["recommended"]; has {
		t.Fatalf("did not expect recommended on option 2, got %#v", opt2["recommended"])
	}
}

func TestAskUserQuestionHandlerValidateArgsRecommendedMultiple(t *testing.T) {
	handler := NewAskUserQuestionHandler()
	err := handler.ValidateArgs(map[string]any{
		"mode": "question",
		"questions": []any{
			map[string]any{
				"question": "Pick a plan",
				"type":     "select",
				"options": []any{
					map[string]any{"label": "A", "recommended": true},
					map[string]any{"label": "B", "recommended": true},
					map[string]any{"label": "C"},
				},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "at most one option can have recommended=true") {
		t.Fatalf("expected error for multiple recommended options, got %v", err)
	}
}

func TestAskUserQuestionHandlerValidateArgsRecommendedMultiSelect(t *testing.T) {
	handler := NewAskUserQuestionHandler()
	err := handler.ValidateArgs(map[string]any{
		"mode": "question",
		"questions": []any{
			map[string]any{
				"question": "Pick topics",
				"type":     "multi-select",
				"options": []any{
					map[string]any{"label": "A", "recommended": true},
					map[string]any{"label": "B"},
				},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "recommended is not allowed for multi-select questions") {
		t.Fatalf("expected error for recommended in multi-select, got %v", err)
	}

	// Also check recommended=false is rejected for multi-select
	err = handler.ValidateArgs(map[string]any{
		"mode": "question",
		"questions": []any{
			map[string]any{
				"question": "Pick topics",
				"type":     "multi-select",
				"options": []any{
					map[string]any{"label": "A", "recommended": false},
				},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "recommended is not allowed for multi-select questions") {
		t.Fatalf("expected error for recommended=false in multi-select, got %v", err)
	}
}

func TestAskUserQuestionHandlerValidateArgsRecommendedNonBool(t *testing.T) {
	handler := NewAskUserQuestionHandler()
	err := handler.ValidateArgs(map[string]any{
		"mode": "question",
		"questions": []any{
			map[string]any{
				"question": "Pick a plan",
				"type":     "select",
				"options": []any{
					map[string]any{"label": "A", "recommended": "yes"},
				},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "recommended must be a boolean") {
		t.Fatalf("expected error for non-boolean recommended, got %v", err)
	}

	// Also check integer
	err = handler.ValidateArgs(map[string]any{
		"mode": "question",
		"questions": []any{
			map[string]any{
				"question": "Pick a plan",
				"type":     "select",
				"options": []any{
					map[string]any{"label": "A", "recommended": 1},
				},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "recommended must be a boolean") {
		t.Fatalf("expected error for integer recommended, got %v", err)
	}
}
