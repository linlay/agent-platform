package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
	. "agent-platform-runner-go/internal/contracts"
)

type FrontendSubmitCoordinator struct{}

func NewFrontendSubmitCoordinator() *FrontendSubmitCoordinator {
	return &FrontendSubmitCoordinator{}
}

func (c *FrontendSubmitCoordinator) Await(ctx context.Context, execCtx *ExecutionContext, args map[string]any) (ToolExecutionResult, error) {
	_ = c
	if execCtx == nil || execCtx.RunControl == nil {
		return ToolExecutionResult{}, ErrRunControlUnavailable
	}
	toolName := execCtx.CurrentToolName
	awaitingID := execCtx.CurrentToolID
	timeout := toolTimeout(NormalizeBudget(execCtx.Budget).Tool)
	execCtx.RunLoopState = RunLoopStateWaitingSubmit
	execCtx.RunControl.TransitionState(RunLoopStateWaitingSubmit)
	waitStarted := time.Now()

	result, err := execCtx.RunControl.AwaitSubmitWithTimeout(ctx, awaitingID, timeout)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			elapsedMs := time.Since(waitStarted).Milliseconds()
			timeoutMs := timeout.Milliseconds()
			payload := NewErrorPayload(
				"frontend_submit_timeout",
				resolveFrontendTimeoutMessage(toolName, awaitingID, timeoutMs, elapsedMs),
				ErrorScopeFrontendSubmit,
				ErrorCategoryTimeout,
				map[string]any{
					"awaitingId": awaitingID,
					"toolName":   toolName,
					"timeoutMs":  timeoutMs,
					"elapsedMs":  elapsedMs,
				},
			)
			return ToolExecutionResult{
				Output:     marshalJSON(payload),
				Structured: payload,
				Error:      "frontend_submit_timeout",
				ExitCode:   -1,
			}, nil
		}
		return ToolExecutionResult{}, err
	}
	execCtx.RunLoopState = RunLoopStateToolExecuting
	execCtx.RunControl.TransitionState(RunLoopStateToolExecuting)

	normalized, normalizeErr := normalizeFrontendSubmitResult(toolName, args, result.Request.Params)
	if normalizeErr != nil {
		payload := NewErrorPayload(
			"frontend_submit_invalid_payload",
			normalizeErr.Error(),
			ErrorScopeFrontendSubmit,
			ErrorCategoryTool,
			map[string]any{
				"awaitingId": awaitingID,
				"toolName":   toolName,
				"params":     result.Request.Params,
			},
		)
		return ToolExecutionResult{
			Output:     marshalJSON(payload),
			Structured: payload,
			Error:      "frontend_submit_invalid_payload",
			ExitCode:   -1,
			SubmitInfo: &SubmitInfo{
				RunID:      result.Request.RunID,
				AwaitingID: result.Request.AwaitingID,
				Params:     result.Request.Params,
			},
		}, nil
	}
	data, _ := json.Marshal(normalized)
	return ToolExecutionResult{
		Output:     string(data),
		Structured: normalized,
		ExitCode:   0,
		SubmitInfo: &SubmitInfo{
			RunID:      result.Request.RunID,
			AwaitingID: result.Request.AwaitingID,
			Params:     result.Request.Params,
		},
	}, nil
}

func resolveFrontendTimeoutMessage(toolName string, awaitingID string, timeoutMs int64, elapsedMs int64) string {
	if toolName == "" {
		toolName = "unknown"
	}
	if awaitingID == "" {
		awaitingID = "unknown"
	}
	return "Frontend tool submit timeout: tool=" + toolName + ", awaitingId=" + awaitingID + ", elapsedMs=" + formatInt64(elapsedMs) + ", timeoutMs=" + formatInt64(timeoutMs)
}

func formatInt64(value int64) string {
	return strconv.FormatInt(value, 10)
}

func marshalJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func NewSteerDelta(req api.SteerRequest) DeltaRequestSteer {
	return DeltaRequestSteer{
		RequestID: req.RequestID,
		ChatID:    req.ChatID,
		RunID:     req.RunID,
		SteerID:   req.SteerID,
		Message:   req.Message,
	}
}

func normalizeFrontendSubmitResult(toolName string, args map[string]any, params any) (map[string]any, error) {
	switch strings.TrimSpace(toolName) {
	case "_ask_user_question_":
		return normalizeAskUserQuestionSubmit(args, params)
	case "_ask_user_approval_":
		return normalizeAskUserApprovalSubmit(args, params)
	default:
		payload, ok := params.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("frontend submit params must be an object")
		}
		return map[string]any{
			"mode":   AnyStringNode(args["mode"]),
			"params": cloneMap(payload),
		}, nil
	}
}

func normalizeAskUserQuestionSubmit(args map[string]any, params any) (map[string]any, error) {
	payload, ok := params.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("ask_user_question submit params must be an object")
	}
	rawAnswers, ok := payload["answers"].([]any)
	if !ok {
		return nil, fmt.Errorf("ask_user_question submit params.answers must be an array")
	}

	questionDefs := map[string]map[string]any{}
	for _, rawQuestion := range asAnySlice(args["questions"]) {
		question := AnyMapNode(rawQuestion)
		text := AnyStringNode(question["question"])
		if text == "" {
			continue
		}
		questionDefs[text] = question
	}

	answers := make([]map[string]any, 0, len(rawAnswers))
	for _, rawAnswer := range rawAnswers {
		answerMap := AnyMapNode(rawAnswer)
		if len(answerMap) == 0 {
			return nil, fmt.Errorf("ask_user_question answers must contain objects")
		}
		questionText := AnyStringNode(answerMap["question"])
		if questionText == "" {
			return nil, fmt.Errorf("ask_user_question answers.question is required")
		}
		definition, ok := questionDefs[questionText]
		if !ok {
			return nil, fmt.Errorf("unknown question: %s", questionText)
		}
		normalizedAnswer, err := normalizeQuestionAnswer(definition, answerMap["answer"])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", questionText, err)
		}
		answers = append(answers, map[string]any{
			"question": questionText,
			"answer":   normalizedAnswer,
		})
	}

	return map[string]any{
		"mode":    "question",
		"answers": answers,
	}, nil
}

func normalizeAskUserApprovalSubmit(args map[string]any, params any) (map[string]any, error) {
	payload, ok := params.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("ask_user_approval submit params must be an object")
	}
	value, hasValue := nonEmptyStringField(payload["value"])
	freeText, hasFreeText := nonEmptyStringField(payload["freeText"])
	if hasValue == hasFreeText {
		return nil, fmt.Errorf("ask_user_approval submit params must contain exactly one of value or freeText")
	}
	if hasValue {
		allowed := map[string]bool{}
		for _, rawOption := range asAnySlice(args["options"]) {
			option := AnyMapNode(rawOption)
			if optionValue := AnyStringNode(option["value"]); optionValue != "" {
				allowed[optionValue] = true
			}
		}
		if len(allowed) > 0 && !allowed[value] {
			return nil, fmt.Errorf("ask_user_approval value is not an allowed option")
		}
		return map[string]any{
			"mode":  "approval",
			"value": value,
		}, nil
	}
	if !AnyBoolNode(args["allowFreeText"]) {
		return nil, fmt.Errorf("ask_user_approval freeText is not allowed")
	}
	return map[string]any{
		"mode":     "approval",
		"freeText": freeText,
	}, nil
}

func normalizeQuestionAnswer(definition map[string]any, rawAnswer any) (any, error) {
	questionType := strings.ToLower(AnyStringNode(definition["type"]))
	switch questionType {
	case "number":
		switch value := rawAnswer.(type) {
		case int, int32, int64, float32, float64, json.Number:
			return value, nil
		default:
			return nil, fmt.Errorf("answer must be a number")
		}
	case "text", "password":
		text := AnyStringNode(rawAnswer)
		if text == "" {
			return nil, fmt.Errorf("answer must be a non-empty string")
		}
		return text, nil
	case "select":
		if AnyBoolNode(definition["multiSelect"]) {
			items, ok := rawAnswer.([]any)
			if !ok {
				return nil, fmt.Errorf("answer must be an array")
			}
			values := make([]string, 0, len(items))
			for _, item := range items {
				text := AnyStringNode(item)
				if text == "" {
					return nil, fmt.Errorf("answer items must be non-empty strings")
				}
				values = append(values, text)
			}
			if len(values) == 0 {
				return nil, fmt.Errorf("answer must not be empty")
			}
			if !AnyBoolNode(definition["allowFreeText"]) {
				allowed := allowedQuestionOptions(definition)
				for _, value := range values {
					if !allowed[value] {
						return nil, fmt.Errorf("answer item %q is not an allowed option", value)
					}
				}
			}
			return values, nil
		}
		text := AnyStringNode(rawAnswer)
		if text == "" {
			return nil, fmt.Errorf("answer must be a non-empty string")
		}
		if !AnyBoolNode(definition["allowFreeText"]) {
			if !allowedQuestionOptions(definition)[text] {
				return nil, fmt.Errorf("answer is not an allowed option")
			}
		}
		return text, nil
	default:
		return rawAnswer, nil
	}
}

func allowedQuestionOptions(definition map[string]any) map[string]bool {
	allowed := map[string]bool{}
	for _, rawOption := range asAnySlice(definition["options"]) {
		option := AnyMapNode(rawOption)
		label := AnyStringNode(option["label"])
		if label != "" {
			allowed[label] = true
		}
	}
	return allowed
}

func nonEmptyStringField(value any) (string, bool) {
	text := AnyStringNode(value)
	return text, text != ""
}

func asAnySlice(value any) []any {
	items, _ := value.([]any)
	return items
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}
