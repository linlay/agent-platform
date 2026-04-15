package frontendtools

import (
	"fmt"
	"strings"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/stream"
)

type AskUserApprovalHandler struct{}

func NewAskUserApprovalHandler() *AskUserApprovalHandler {
	return &AskUserApprovalHandler{}
}

func (h *AskUserApprovalHandler) ToolName() string {
	return "_ask_user_approval_"
}

func (h *AskUserApprovalHandler) BuildInitialAwaitAsk(_ string, _ string, _ api.ToolDetailResponse, _ int, _ int64) *stream.AwaitAsk {
	return nil
}

func (h *AskUserApprovalHandler) BuildDeferredAwait(toolID string, runID string, tool api.ToolDetailResponse, args map[string]any, timeoutMs int64) []contracts.AgentDelta {
	if !strings.EqualFold(strings.TrimSpace(contracts.AnyStringNode(args["mode"])), "approval") {
		return nil
	}
	viewportType, _ := tool.Meta["viewportType"].(string)
	viewportKey, _ := tool.Meta["viewportKey"].(string)
	question := map[string]any{}
	if value := contracts.AnyStringNode(args["question"]); value != "" {
		question["question"] = value
	}
	if value := contracts.AnyStringNode(args["header"]); value != "" {
		question["header"] = value
	}
	if value := contracts.AnyStringNode(args["description"]); value != "" {
		question["description"] = value
	}
	if options, ok := args["options"].([]any); ok {
		question["options"] = cloneAwaitQuestions(options)
	}
	if _, exists := args["allowFreeText"]; exists {
		question["allowFreeText"] = args["allowFreeText"]
	}
	if value := contracts.AnyStringNode(args["freeTextPlaceholder"]); value != "" {
		question["freeTextPlaceholder"] = value
	}
	return []contracts.AgentDelta{contracts.DeltaAwaitAsk{
		AwaitingID:   toolID,
		ViewportType: strings.TrimSpace(viewportType),
		ViewportKey:  strings.TrimSpace(viewportKey),
		Mode:         "approval",
		ToolTimeout:  timeoutMs,
		RunID:        runID,
		Questions:    []any{question},
	}}
}

func (h *AskUserApprovalHandler) NormalizeSubmit(args map[string]any, params any) (map[string]any, error) {
	payload, ok := params.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("ask_user_approval submit params must be an object")
	}
	value, hasValue := nonEmptyStringField(payload["value"])
	freeText, hasFreeText := nonEmptyStringField(payload["freeText"])
	if !hasValue && !hasFreeText {
		return map[string]any{
			"mode":      "approval",
			"cancelled": true,
			"reason":    "user_dismissed",
		}, nil
	}
	if hasValue && hasFreeText {
		return nil, fmt.Errorf("ask_user_approval submit params must contain exactly one of value or freeText")
	}
	if hasValue {
		allowed := map[string]bool{}
		for _, rawOption := range asAnySlice(args["options"]) {
			option := contracts.AnyMapNode(rawOption)
			if optionValue := contracts.AnyStringNode(option["value"]); optionValue != "" {
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
	if !contracts.AnyBoolNode(args["allowFreeText"]) {
		return nil, fmt.Errorf("ask_user_approval freeText is not allowed")
	}
	return map[string]any{
		"mode":     "approval",
		"freeText": freeText,
	}, nil
}

func (h *AskUserApprovalHandler) FormatSubmitResult(format string, result contracts.ToolExecutionResult) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "summary", "qa":
		return formatApprovalSummary(result), true
	case "kv":
		return formatApprovalKV(result), true
	default:
		return "", false
	}
}

func formatApprovalSummary(result contracts.ToolExecutionResult) string {
	if value := strings.TrimSpace(contracts.AnyStringNode(result.Structured["value"])); value != "" {
		return "用户选择了: " + value
	}
	if freeText := strings.TrimSpace(contracts.AnyStringNode(result.Structured["freeText"])); freeText != "" {
		return "用户自由输入: " + freeText
	}
	return result.Output
}

func formatApprovalKV(result contracts.ToolExecutionResult) string {
	if value := strings.TrimSpace(contracts.AnyStringNode(result.Structured["value"])); value != "" {
		return "选择=" + value
	}
	if freeText := strings.TrimSpace(contracts.AnyStringNode(result.Structured["freeText"])); freeText != "" {
		return "自由输入=" + freeText
	}
	return result.Output
}

func nonEmptyStringField(value any) (string, bool) {
	text := contracts.AnyStringNode(value)
	return text, text != ""
}
