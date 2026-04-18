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

func TestAskUserQuestionHandlerValidateArgs(t *testing.T) {
	handler := NewAskUserQuestionHandler()

	validArgs := map[string]any{
		"mode": "question",
		"questions": []any{
			map[string]any{
				"question": "Pick a plan",
				"type":     "select",
				"options": []any{
					map[string]any{"label": "Weekend"},
				},
			},
			map[string]any{
				"question": "How many people?",
				"type":     "number",
			},
		},
	}
	if err := handler.ValidateArgs(validArgs); err != nil {
		t.Fatalf("ValidateArgs returned error for valid args: %v", err)
	}

	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "select missing options",
			args: map[string]any{
				"mode": "question",
				"questions": []any{
					map[string]any{"question": "Pick a plan", "type": "select"},
				},
			},
			want: "options is required for select questions",
		},
		{
			name: "select empty options",
			args: map[string]any{
				"mode": "question",
				"questions": []any{
					map[string]any{"question": "Pick a plan", "type": "select", "options": []any{}},
				},
			},
			want: "options is required for select questions",
		},
		{
			name: "select blank option label",
			args: map[string]any{
				"mode": "question",
				"questions": []any{
					map[string]any{
						"question": "Pick a plan",
						"type":     "select",
						"options": []any{
							map[string]any{"label": " "},
						},
					},
				},
			},
			want: "option 1 label is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := handler.ValidateArgs(tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
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

func TestAskUserApprovalHandlerBuildDeferredAwaitForFormViewport(t *testing.T) {
	handler := NewAskUserApprovalHandler()
	deltas := handler.BuildDeferredAwait("tool_1", "run_1", frontendTool("_ask_user_approval_"), map[string]any{
		"mode":         "approval",
		"viewportType": "html",
		"viewportKey":  "leave_form",
		"command":      `mock create-leave --payload '{"employee_id":"E1001"}'`,
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
	if awaitAsk.Command != `mock create-leave --payload '{"employee_id":"E1001"}'` {
		t.Fatalf("expected command to be forwarded, got %#v", awaitAsk)
	}
	if len(awaitAsk.Questions) != 0 {
		t.Fatalf("expected form approval to omit questions, got %#v", awaitAsk.Questions)
	}
}

func TestAskUserApprovalHandlerNormalizeSubmitForFormViewport(t *testing.T) {
	handler := NewAskUserApprovalHandler()
	result, err := handler.NormalizeSubmit(map[string]any{
		"mode":         "approval",
		"viewportType": "html",
		"command":      `mock create-leave --payload '{"employee_id":"E1001"}'`,
	}, map[string]any{
		"action": "submit",
		"payload": map[string]any{
			"employee_id": "E1001",
			"days":        2,
		},
	})
	if err != nil {
		t.Fatalf("NormalizeSubmit returned error: %v", err)
	}
	if result["mode"] != "approval" || result["action"] != "submit" {
		t.Fatalf("unexpected normalized form submit %#v", result)
	}
	payload, ok := result["payload"].(map[string]any)
	if !ok || payload["employee_id"] != "E1001" || payload["days"] != 2 {
		t.Fatalf("expected normalized payload, got %#v", result)
	}
	if _, exists := result["questions"]; exists {
		t.Fatalf("did not expect questions in form submit result, got %#v", result)
	}
}

func TestAskUserApprovalHandlerNormalizeSubmitAcceptsLegacyPresetValueWithoutSubmittedValue(t *testing.T) {
	handler := NewAskUserApprovalHandler()
	result, err := handler.NormalizeSubmit(map[string]any{
		"questions": []any{
			map[string]any{
				"question": "docker rmi busybox:latest 2>&1",
				"header":   "Bash Approval",
				"options": []any{
					map[string]any{"label": "Approve", "value": "approve"},
					map[string]any{"label": "Reject", "value": "reject"},
				},
			},
		},
	}, []any{
		map[string]any{"question": "docker rmi busybox:latest 2>&1", "answer": "approve"},
	})
	if err != nil {
		t.Fatalf("NormalizeSubmit returned error: %v", err)
	}
	questions, ok := result["questions"].([]map[string]any)
	if !ok || len(questions) != 1 {
		t.Fatalf("expected normalized approval questions, got %#v", result)
	}
	if questions[0]["answer"] != "Approve" || questions[0]["value"] != "approve" {
		t.Fatalf("expected legacy preset submit to normalize to label/value, got %#v", questions[0])
	}
}

func TestAskUserApprovalHandlerNormalizeSubmitAcceptsLegacyPresetValuePair(t *testing.T) {
	handler := NewAskUserApprovalHandler()
	result, err := handler.NormalizeSubmit(map[string]any{
		"questions": []any{
			map[string]any{
				"question": "docker rmi busybox:latest 2>&1",
				"header":   "Bash Approval",
				"options": []any{
					map[string]any{"label": "Approve", "value": "approve"},
					map[string]any{"label": "Reject", "value": "reject"},
				},
			},
		},
	}, []any{
		map[string]any{"question": "docker rmi busybox:latest 2>&1", "answer": "approve", "value": "approve"},
	})
	if err != nil {
		t.Fatalf("NormalizeSubmit returned error: %v", err)
	}
	questions, ok := result["questions"].([]map[string]any)
	if !ok || len(questions) != 1 {
		t.Fatalf("expected normalized approval questions, got %#v", result)
	}
	if questions[0]["answer"] != "Approve" || questions[0]["value"] != "approve" {
		t.Fatalf("expected legacy preset pair to normalize to label/value, got %#v", questions[0])
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

	formResult := contracts.ToolExecutionResult{
		Output: `{"mode":"approval","action":"submit","payload":{"days":2,"employee_id":"E1001"}}`,
		Structured: map[string]any{
			"mode":   "approval",
			"action": "submit",
			"payload": map[string]any{
				"days":        2,
				"employee_id": "E1001",
			},
		},
	}
	if got, ok := handler.FormatSubmitResult("summary", formResult); !ok || got != "用户提交了表单:\n- days: 2\n- employee_id: E1001" {
		t.Fatalf("unexpected form summary result: ok=%v got=%q", ok, got)
	}
	if got, ok := handler.FormatSubmitResult("kv", formResult); !ok || got != "days=2; employee_id=E1001" {
		t.Fatalf("unexpected form kv result: ok=%v got=%q", ok, got)
	}
	if got, ok := handler.FormatSubmitResult("qa", formResult); !ok || got != "字段：days\n值：2\n字段：employee_id\n值：E1001" {
		t.Fatalf("unexpected form qa result: ok=%v got=%q", ok, got)
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

func TestAskUserApprovalHandlerNormalizeSubmitForFormCancel(t *testing.T) {
	handler := NewAskUserApprovalHandler()
	result, err := handler.NormalizeSubmit(map[string]any{
		"mode":         "approval",
		"viewportType": "html",
		"command":      `mock create-leave --payload '{"employee_id":"E1001"}'`,
	}, map[string]any{
		"action": "cancel",
	})
	if err != nil {
		t.Fatalf("NormalizeSubmit returned error: %v", err)
	}
	expected := map[string]any{
		"mode":      "approval",
		"cancelled": true,
		"reason":    "user_cancelled",
	}
	if !reflect.DeepEqual(result, expected) {
		t.Fatalf("expected cancel payload %#v, got %#v", expected, result)
	}
}

func TestAskUserApprovalHandlerNormalizeSubmitAcceptsApproveAlways(t *testing.T) {
	handler := NewAskUserApprovalHandler()
	result, err := handler.NormalizeSubmit(map[string]any{
		"questions": []any{
			map[string]any{
				"question": "docker rmi busybox:latest 2>&1",
				"header":   "Bash Approval",
				"options": []any{
					map[string]any{"label": "Approve", "value": "approve"},
					map[string]any{"label": "Always Approve", "value": "approve_always"},
					map[string]any{"label": "Reject", "value": "reject"},
				},
			},
		},
	}, []any{
		map[string]any{"question": "docker rmi busybox:latest 2>&1", "answer": "Always Approve", "value": "approve_always"},
	})
	if err != nil {
		t.Fatalf("NormalizeSubmit returned error: %v", err)
	}
	questions, ok := result["questions"].([]map[string]any)
	if !ok || len(questions) != 1 {
		t.Fatalf("expected normalized approval questions, got %#v", result)
	}
	if questions[0]["answer"] != "Always Approve" || questions[0]["value"] != "approve_always" {
		t.Fatalf("expected approve_always to normalize, got %#v", questions[0])
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

func TestAskUserApprovalHandlerRejectsConflictingLegacyValueAndSubmittedValue(t *testing.T) {
	handler := NewAskUserApprovalHandler()
	_, err := handler.NormalizeSubmit(map[string]any{
		"questions": []any{
			map[string]any{
				"question": "Need confirmation",
				"options": []any{
					map[string]any{"label": "Approve", "value": "approve"},
					map[string]any{"label": "Reject", "value": "reject"},
				},
			},
		},
	}, []any{
		map[string]any{"question": "Need confirmation", "answer": "reject", "value": "approve"},
	})
	if err == nil || !strings.Contains(err.Error(), "value does not match selected option") {
		t.Fatalf("expected conflicting legacy value pair to be rejected, got %v", err)
	}
}
