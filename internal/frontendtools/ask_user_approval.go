package frontendtools

import (
	"fmt"
	"sort"
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

func (h *AskUserApprovalHandler) ValidateArgs(args map[string]any) error {
	return nil
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
	if value := contracts.AnyStringNode(args["viewportType"]); value != "" {
		viewportType = value
	}
	if value := contracts.AnyStringNode(args["viewportKey"]); value != "" {
		viewportKey = value
	}
	command := strings.TrimSpace(contracts.AnyStringNode(args["command"]))
	if strings.EqualFold(strings.TrimSpace(viewportType), "html") && command != "" {
		return []contracts.AgentDelta{contracts.DeltaAwaitAsk{
			AwaitingID:   toolID,
			ViewportType: strings.TrimSpace(viewportType),
			ViewportKey:  strings.TrimSpace(viewportKey),
			Mode:         "approval",
			Timeout:      timeoutMs,
			RunID:        runID,
			Command:      command,
		}}
	}
	questions := cloneAwaitQuestions(asAnySlice(args["questions"]))
	if len(questions) == 0 {
		return nil
	}
	return []contracts.AgentDelta{contracts.DeltaAwaitAsk{
		AwaitingID:   toolID,
		ViewportType: strings.TrimSpace(viewportType),
		ViewportKey:  strings.TrimSpace(viewportKey),
		Mode:         "approval",
		Timeout:      timeoutMs,
		RunID:        runID,
		Questions:    questions,
	}}
}

func (h *AskUserApprovalHandler) NormalizeSubmit(args map[string]any, params any) (map[string]any, error) {
	if isFormApprovalArgs(args) {
		return normalizeFormApprovalSubmit(params)
	}

	rawAnswers, ok := params.([]any)
	if !ok {
		return nil, fmt.Errorf("ask_user_approval submit params must be an array")
	}
	if len(rawAnswers) == 0 {
		return map[string]any{
			"mode":      "approval",
			"cancelled": true,
			"reason":    "user_dismissed",
		}, nil
	}

	questionDefs := map[string]map[string]any{}
	for _, rawQuestion := range asAnySlice(args["questions"]) {
		question := contracts.AnyMapNode(rawQuestion)
		text := contracts.AnyStringNode(question["question"])
		if text == "" {
			continue
		}
		questionDefs[text] = question
	}

	seenQuestions := map[string]bool{}
	questions := make([]map[string]any, 0, len(rawAnswers))
	for _, rawAnswer := range rawAnswers {
		answerMap := contracts.AnyMapNode(rawAnswer)
		if len(answerMap) == 0 {
			return nil, fmt.Errorf("ask_user_approval answers must contain objects")
		}
		questionText := contracts.AnyStringNode(answerMap["question"])
		if questionText == "" {
			return nil, fmt.Errorf("ask_user_approval answers.question is required")
		}
		if seenQuestions[questionText] {
			return nil, fmt.Errorf("duplicate question: %s", questionText)
		}
		definition, ok := questionDefs[questionText]
		if !ok {
			return nil, fmt.Errorf("unknown question: %s", questionText)
		}
		answerText := contracts.AnyStringNode(answerMap["answer"])
		if answerText == "" {
			return nil, fmt.Errorf("%s: answer is required", questionText)
		}
		normalizedQuestion, err := normalizeApprovalQuestion(definition, answerText, answerMap)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", questionText, err)
		}
		questions = append(questions, normalizedQuestion)
		seenQuestions[questionText] = true
	}

	return map[string]any{
		"mode":      "approval",
		"questions": questions,
	}, nil
}

func isFormApprovalArgs(args map[string]any) bool {
	return strings.EqualFold(strings.TrimSpace(contracts.AnyStringNode(args["viewportType"])), "html") &&
		strings.TrimSpace(contracts.AnyStringNode(args["command"])) != ""
}

func normalizeFormApprovalSubmit(params any) (map[string]any, error) {
	payload := contracts.AnyMapNode(params)
	if len(payload) == 0 {
		return nil, fmt.Errorf("ask_user_approval form submit params must be an object")
	}

	action := strings.ToLower(strings.TrimSpace(contracts.AnyStringNode(payload["action"])))
	switch action {
	case "cancel":
		return map[string]any{
			"mode":      "approval",
			"cancelled": true,
			"reason":    "user_cancelled",
		}, nil
	case "submit":
		formPayload := contracts.AnyMapNode(payload["payload"])
		if formPayload == nil {
			return nil, fmt.Errorf("ask_user_approval form submit payload must be an object")
		}
		return map[string]any{
			"mode":    "approval",
			"action":  "submit",
			"payload": formPayload,
		}, nil
	default:
		return nil, fmt.Errorf("ask_user_approval form action must be submit or cancel")
	}
}

func (h *AskUserApprovalHandler) FormatSubmitResult(format string, result contracts.ToolExecutionResult) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "summary":
		return formatApprovalSummary(result), true
	case "kv":
		return formatApprovalKV(result), true
	case "qa":
		return formatApprovalQA(result), true
	default:
		return "", false
	}
}

func normalizeApprovalQuestion(definition map[string]any, answerText string, answerMap map[string]any) (map[string]any, error) {
	questionText := contracts.AnyStringNode(definition["question"])
	entry := map[string]any{
		"question": questionText,
		"header":   contracts.AnyStringNode(definition["header"]),
		"answer":   answerText,
	}

	if option, ok := approvalOptionByLabel(definition, answerText); ok {
		submittedValue := contracts.AnyStringNode(answerMap["value"])
		if submittedValue == "" {
			return nil, fmt.Errorf("value is required for preset options")
		}
		if submittedValue != option.Value {
			return nil, fmt.Errorf("value does not match selected option")
		}
		entry["answer"] = option.Label
		entry["value"] = option.Value
		return entry, nil
	}

	if option, ok := approvalOptionByValue(definition, answerText); ok {
		submittedValue := contracts.AnyStringNode(answerMap["value"])
		if submittedValue == "" {
			submittedValue = option.Value
		}
		if submittedValue != option.Value {
			return nil, fmt.Errorf("value does not match selected option")
		}
		entry["answer"] = option.Label
		entry["value"] = option.Value
		return entry, nil
	}

	if !contracts.AnyBoolNode(definition["allowFreeText"]) {
		return nil, fmt.Errorf("answer is not an allowed option")
	}
	submittedValue := contracts.AnyStringNode(answerMap["value"])
	if submittedValue == "" {
		submittedValue = answerText
	}
	if submittedValue != answerText {
		return nil, fmt.Errorf("value must match answer for free-text approval")
	}
	entry["value"] = submittedValue
	return entry, nil
}

type approvalOption struct {
	Label string
	Value string
}

func approvalOptionByLabel(definition map[string]any, label string) (approvalOption, bool) {
	for _, rawOption := range asAnySlice(definition["options"]) {
		option := contracts.AnyMapNode(rawOption)
		candidate := approvalOption{
			Label: contracts.AnyStringNode(option["label"]),
			Value: contracts.AnyStringNode(option["value"]),
		}
		if candidate.Label == "" || candidate.Value == "" {
			continue
		}
		if candidate.Label == label {
			return candidate, true
		}
	}
	return approvalOption{}, false
}

func approvalOptionByValue(definition map[string]any, value string) (approvalOption, bool) {
	for _, rawOption := range asAnySlice(definition["options"]) {
		option := contracts.AnyMapNode(rawOption)
		candidate := approvalOption{
			Label: contracts.AnyStringNode(option["label"]),
			Value: contracts.AnyStringNode(option["value"]),
		}
		if candidate.Label == "" || candidate.Value == "" {
			continue
		}
		if candidate.Value == value {
			return candidate, true
		}
	}
	return approvalOption{}, false
}

func formatApprovalSummary(result contracts.ToolExecutionResult) string {
	if summary, ok := formatFormApprovalSummary(result); ok {
		return summary
	}
	questions, ok := structuredQuestions(result)
	if !ok || len(questions) == 0 {
		return result.Output
	}
	lines := make([]string, 0, len(questions)+1)
	lines = append(lines, "用户完成了以下确认:")
	for _, question := range questions {
		key := formatAnswerKey(question)
		if key == "" {
			return result.Output
		}
		lines = append(lines, "- "+key+": "+formatAnswerValue(question["answer"]))
	}
	return strings.Join(lines, "\n")
}

func formatApprovalKV(result contracts.ToolExecutionResult) string {
	if summary, ok := formatFormApprovalKV(result); ok {
		return summary
	}
	questions, ok := structuredQuestions(result)
	if !ok || len(questions) == 0 {
		return result.Output
	}
	items := make([]string, 0, len(questions))
	for _, question := range questions {
		key := formatAnswerKey(question)
		if key == "" {
			return result.Output
		}
		items = append(items, key+"="+formatAnswerValue(question["answer"]))
	}
	return strings.Join(items, "; ")
}

func formatApprovalQA(result contracts.ToolExecutionResult) string {
	if summary, ok := formatFormApprovalQA(result); ok {
		return summary
	}
	questions, ok := structuredQuestions(result)
	if !ok || len(questions) == 0 {
		return result.Output
	}
	lines := make([]string, 0, len(questions)*2)
	for _, question := range questions {
		prompt := strings.TrimSpace(contracts.AnyStringNode(question["question"]))
		if prompt == "" {
			prompt = strings.TrimSpace(contracts.AnyStringNode(question["header"]))
		}
		if prompt == "" {
			return result.Output
		}
		lines = append(lines, "问题："+prompt)
		lines = append(lines, "回答："+formatAnswerValue(question["answer"]))
	}
	return strings.Join(lines, "\n")
}

func formatFormApprovalSummary(result contracts.ToolExecutionResult) (string, bool) {
	if formPayload, ok := structuredApprovalPayload(result); ok {
		lines := make([]string, 0, len(formPayload)+1)
		lines = append(lines, "用户提交了表单:")
		for _, entry := range formatPayloadEntries(formPayload) {
			lines = append(lines, "- "+entry.summary)
		}
		return strings.Join(lines, "\n"), true
	}
	if result.Structured["cancelled"] == true {
		return "用户取消了表单提交", true
	}
	return "", false
}

func formatFormApprovalKV(result contracts.ToolExecutionResult) (string, bool) {
	if formPayload, ok := structuredApprovalPayload(result); ok {
		entries := formatPayloadEntries(formPayload)
		items := make([]string, 0, len(entries))
		for _, entry := range entries {
			items = append(items, entry.kv)
		}
		return strings.Join(items, "; "), true
	}
	if result.Structured["cancelled"] == true {
		return "status=cancelled; reason=" + formatAnswerValue(result.Structured["reason"]), true
	}
	return "", false
}

func formatFormApprovalQA(result contracts.ToolExecutionResult) (string, bool) {
	if formPayload, ok := structuredApprovalPayload(result); ok {
		entries := formatPayloadEntries(formPayload)
		lines := make([]string, 0, len(entries)*2)
		for _, entry := range entries {
			lines = append(lines, "字段："+entry.key)
			lines = append(lines, "值："+entry.value)
		}
		return strings.Join(lines, "\n"), true
	}
	if result.Structured["cancelled"] == true {
		return "字段：status\n值：cancelled", true
	}
	return "", false
}

func structuredApprovalPayload(result contracts.ToolExecutionResult) (map[string]any, bool) {
	if !strings.EqualFold(strings.TrimSpace(contracts.AnyStringNode(result.Structured["action"])), "submit") {
		return nil, false
	}
	payload := contracts.AnyMapNode(result.Structured["payload"])
	if payload == nil {
		return nil, false
	}
	return payload, true
}

type formattedPayloadEntry struct {
	key     string
	value   string
	summary string
	kv      string
}

func formatPayloadEntries(payload map[string]any) []formattedPayloadEntry {
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	entries := make([]formattedPayloadEntry, 0, len(keys))
	for _, key := range keys {
		value := formatAnswerValue(payload[key])
		entries = append(entries, formattedPayloadEntry{
			key:     key,
			value:   value,
			summary: key + ": " + value,
			kv:      key + "=" + value,
		})
	}
	return entries
}
