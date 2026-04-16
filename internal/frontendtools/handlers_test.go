package frontendtools

import (
	"reflect"
	"strings"
	"testing"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/contracts"
)

func frontendTool(name string) api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Name: name,
		Meta: map[string]any{
			"kind":         "frontend",
			"viewportType": "builtin",
			"viewportKey":  "confirm_dialog",
		},
	}
}

func TestAskUserQuestionHandlerBuildInitialAwaitAsk(t *testing.T) {
	handler := NewAskUserQuestionHandler()
	awaitAsk := handler.BuildInitialAwaitAsk("tool_1", "run_1", frontendTool("_ask_user_question_"), 0, 5000)
	if awaitAsk == nil {
		t.Fatal("expected initial await ask")
	}
	if awaitAsk.Mode != "question" || awaitAsk.AwaitingID != "tool_1" || awaitAsk.ViewportKey != "confirm_dialog" {
		t.Fatalf("unexpected await ask %#v", awaitAsk)
	}
	if handler.BuildInitialAwaitAsk("tool_1", "run_1", frontendTool("_ask_user_question_"), 1, 5000) != nil {
		t.Fatal("did not expect later chunks to emit initial await ask")
	}
}

func TestAskUserQuestionHandlerBuildDeferredAwaitSanitizesQuestions(t *testing.T) {
	handler := NewAskUserQuestionHandler()
	args := map[string]any{
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
	}
	deltas := handler.BuildDeferredAwait("tool_1", "run_1", frontendTool("_ask_user_question_"), args, 5000)
	if len(deltas) != 1 {
		t.Fatalf("expected one delta, got %#v", deltas)
	}
	payload, ok := deltas[0].(contracts.DeltaAwaitPayload)
	if !ok {
		t.Fatalf("expected DeltaAwaitPayload, got %#v", deltas[0])
	}
	question := payload.Questions[0].(map[string]any)
	if _, ok := question["allowFreeText"]; ok {
		t.Fatalf("expected question to be sanitized, got %#v", question)
	}

	original := args["questions"].([]any)[0].(map[string]any)
	if _, ok := original["allowFreeText"]; !ok {
		t.Fatal("expected original payload to remain unchanged")
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
				"options": []any{
					map[string]any{"label": "Weekend"},
				},
			},
			map[string]any{
				"question":    "Notification topics",
				"type":        "select",
				"header":      "通知内容",
				"multiSelect": true,
				"options": []any{
					map[string]any{"label": "产品更新"},
					map[string]any{"label": "使用教程"},
				},
			},
		},
	}
	result, err := handler.NormalizeSubmit(args, []any{
		map[string]any{"question": "Pick a plan", "answer": "Weekend"},
		map[string]any{"question": "Notification topics", "answers": []any{"产品更新", "使用教程"}},
	})
	if err != nil {
		t.Fatalf("NormalizeSubmit returned error: %v", err)
	}
	answers, ok := result["answers"].([]map[string]any)
	if !ok || len(answers) != 2 {
		t.Fatalf("expected normalized answers, got %#v", result["answers"])
	}
	if answers[0]["header"] != "行程安排" {
		t.Fatalf("expected first header to be preserved, got %#v", answers[0])
	}
	if !reflect.DeepEqual(answers[1]["answer"], []string{"产品更新", "使用教程"}) {
		t.Fatalf("expected multi-select slice, got %#v", answers[1]["answer"])
	}
}

func TestAskUserQuestionHandlerRejectsInvalidAnswerFields(t *testing.T) {
	handler := NewAskUserQuestionHandler()
	_, err := handler.NormalizeSubmit(map[string]any{
		"questions": []any{
			map[string]any{
				"question":    "Notification topics",
				"type":        "select",
				"multiSelect": true,
			},
		},
	}, []any{
		map[string]any{"question": "Notification topics", "answer": []any{"产品更新"}},
	})
	if err == nil || !strings.Contains(err.Error(), "answers is required for multi-select questions") {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = handler.NormalizeSubmit(map[string]any{
		"questions": []any{
			map[string]any{
				"question": "Pick a plan",
				"type":     "select",
			},
		},
	}, []any{
		map[string]any{"question": "Pick a plan", "answers": []any{"Weekend"}},
	})
	if err == nil || !strings.Contains(err.Error(), "answers is only allowed for multi-select questions") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAskUserQuestionHandlerFormatSubmitResult(t *testing.T) {
	handler := NewAskUserQuestionHandler()
	result := contracts.ToolExecutionResult{
		Output: `{"mode":"question"}`,
		Structured: map[string]any{
			"answers": []any{
				map[string]any{"question": "Pick a plan", "header": "行程安排", "answer": "Weekend"},
				map[string]any{"question": "Subscription topics", "header": "订阅内容", "answer": []any{"产品更新", "使用教程"}},
			},
		},
	}
	if got, ok := handler.FormatSubmitResult("summary", result); !ok || got != "用户回答了以下问题:\n- 行程安排: Weekend\n- 订阅内容: 产品更新, 使用教程" {
		t.Fatalf("unexpected summary result: ok=%v got=%q", ok, got)
	}
	if got, ok := handler.FormatSubmitResult("kv", result); !ok || got != "行程安排=Weekend; 订阅内容=产品更新, 使用教程" {
		t.Fatalf("unexpected kv result: ok=%v got=%q", ok, got)
	}
	if got, ok := handler.FormatSubmitResult("qa", result); !ok || got != "问题：Pick a plan\n回答：Weekend\n问题：Subscription topics\n回答：产品更新, 使用教程" {
		t.Fatalf("unexpected qa result: ok=%v got=%q", ok, got)
	}
}

func TestAskUserApprovalHandlerBuildDeferredAwaitAndNormalizeSubmit(t *testing.T) {
	handler := NewAskUserApprovalHandler()
	deltas := handler.BuildDeferredAwait("tool_1", "run_1", frontendTool("_ask_user_approval_"), map[string]any{
		"mode":         "approval",
		"viewportType": "html",
		"viewportKey":  "leave_form",
		"questions": []any{
			map[string]any{
				"question":            "Need confirmation",
				"header":              "安全检查",
				"allowFreeText":       true,
				"freeTextPlaceholder": "Type your own answer",
				"options": []any{
					map[string]any{"label": "Approve", "value": "approve"},
				},
			},
			map[string]any{
				"question": "Secondary confirmation",
				"options": []any{
					map[string]any{"label": "Reject", "value": "reject"},
				},
			},
		},
	}, 5000)
	if len(deltas) != 1 {
		t.Fatalf("expected one delta, got %#v", deltas)
	}
	awaitAsk, ok := deltas[0].(contracts.DeltaAwaitAsk)
	if !ok {
		t.Fatalf("expected DeltaAwaitAsk, got %#v", deltas[0])
	}
	if awaitAsk.Mode != "approval" || awaitAsk.ViewportKey != "leave_form" || awaitAsk.ViewportType != "html" {
		t.Fatalf("unexpected await ask %#v", awaitAsk)
	}
	if len(awaitAsk.Questions) != 2 {
		t.Fatalf("expected all approval questions to be forwarded, got %#v", awaitAsk.Questions)
	}

	result, err := handler.NormalizeSubmit(map[string]any{
		"questions": []any{
			map[string]any{
				"question": "Need confirmation",
				"header":   "安全检查",
				"options": []any{
					map[string]any{"label": "Approve", "value": "approve"},
				},
			},
			map[string]any{
				"question":      "Edited command",
				"header":        "命令修改",
				"allowFreeText": true,
			},
		},
	}, []any{
		map[string]any{"question": "Need confirmation", "answer": "Approve", "value": "approve"},
		map[string]any{"question": "Edited command", "answer": "later"},
	})
	if err != nil {
		t.Fatalf("NormalizeSubmit returned error: %v", err)
	}
	questions, ok := result["questions"].([]map[string]any)
	if !ok || len(questions) != 2 {
		t.Fatalf("expected normalized approval questions, got %#v", result)
	}
	if questions[0]["answer"] != "Approve" || questions[0]["value"] != "approve" {
		t.Fatalf("expected preset option approval result, got %#v", questions[0])
	}
	if questions[1]["answer"] != "later" || questions[1]["value"] != "later" {
		t.Fatalf("expected free-text approval value to default from answer, got %#v", questions[1])
	}
}

func TestAskUserApprovalHandlerFormatSubmitResult(t *testing.T) {
	handler := NewAskUserApprovalHandler()
	valueResult := contracts.ToolExecutionResult{
		Output: `{"mode":"approval","questions":[{"question":"Need confirmation","header":"安全检查","answer":"Approve","value":"approve"},{"question":"Edited command","answer":"later","value":"later"}]}`,
		Structured: map[string]any{
			"questions": []any{
				map[string]any{"question": "Need confirmation", "header": "安全检查", "answer": "Approve", "value": "approve"},
				map[string]any{"question": "Edited command", "answer": "later", "value": "later"},
			},
		},
	}
	if got, ok := handler.FormatSubmitResult("summary", valueResult); !ok || got != "用户完成了以下确认:\n- 安全检查: Approve\n- Edited command: later" {
		t.Fatalf("unexpected summary result: ok=%v got=%q", ok, got)
	}
	if got, ok := handler.FormatSubmitResult("kv", valueResult); !ok || got != "安全检查=Approve; Edited command=later" {
		t.Fatalf("unexpected kv result: ok=%v got=%q", ok, got)
	}
	if got, ok := handler.FormatSubmitResult("qa", valueResult); !ok || got != "问题：Need confirmation\n回答：Approve\n问题：Edited command\n回答：later" {
		t.Fatalf("unexpected qa result: ok=%v got=%q", ok, got)
	}
}

func TestAskUserApprovalHandlerRejectsLegacyObjectSubmit(t *testing.T) {
	handler := NewAskUserApprovalHandler()
	_, err := handler.NormalizeSubmit(map[string]any{
		"questions": []any{
			map[string]any{
				"question": "Need confirmation",
				"options": []any{
					map[string]any{"label": "Approve", "value": "approve"},
				},
			},
		},
	}, map[string]any{"value": "approve"})
	if err == nil || !strings.Contains(err.Error(), "submit params must be an array") {
		t.Fatalf("expected legacy object submit to be rejected, got %v", err)
	}
}

func TestAskUserApprovalHandlerRejectsMismatchedLabelAndValue(t *testing.T) {
	handler := NewAskUserApprovalHandler()
	_, err := handler.NormalizeSubmit(map[string]any{
		"questions": []any{
			map[string]any{
				"question": "Need confirmation",
				"options": []any{
					map[string]any{"label": "Approve", "value": "approve"},
				},
			},
		},
	}, []any{
		map[string]any{"question": "Need confirmation", "answer": "Approve", "value": "reject"},
	})
	if err == nil || !strings.Contains(err.Error(), "value does not match selected option") {
		t.Fatalf("expected mismatched label/value to be rejected, got %v", err)
	}
}
