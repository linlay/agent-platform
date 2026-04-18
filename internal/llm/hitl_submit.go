package llm

import (
	"fmt"
	"strings"

	contracts "agent-platform-runner-go/internal/contracts"
)

func normalizeHITLSubmit(args map[string]any, params any) (map[string]any, error) {
	if isHITLFormApprovalArgs(args) {
		return normalizeHITLFormSubmit(params)
	}
	return normalizeHITLConfirmSubmit(args, params)
}

func isHITLFormApprovalArgs(args map[string]any) bool {
	return strings.EqualFold(strings.TrimSpace(contracts.AnyStringNode(args["viewportType"])), "html") &&
		contracts.AnyMapNode(args["payload"]) != nil
}

func normalizeHITLFormSubmit(params any) (map[string]any, error) {
	payload := contracts.AnyMapNode(params)
	if len(payload) == 0 {
		return nil, fmt.Errorf("bash HITL form submit params must be an object")
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
			return nil, fmt.Errorf("bash HITL form submit payload must be an object")
		}
		return map[string]any{
			"mode":    "approval",
			"action":  "submit",
			"payload": formPayload,
		}, nil
	default:
		return nil, fmt.Errorf("bash HITL form action must be submit or cancel")
	}
}

func normalizeHITLConfirmSubmit(args map[string]any, params any) (map[string]any, error) {
	rawAnswers, ok := params.([]any)
	if !ok {
		return nil, fmt.Errorf("bash HITL confirm submit params must be an array")
	}
	if len(rawAnswers) == 0 {
		return map[string]any{
			"mode":      "approval",
			"cancelled": true,
			"reason":    "user_dismissed",
		}, nil
	}

	questionDefs := map[string]map[string]any{}
	for _, rawQuestion := range cloneAnySlice(args["questions"]) {
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
			return nil, fmt.Errorf("bash HITL confirm answers must contain objects")
		}
		questionText := contracts.AnyStringNode(answerMap["question"])
		if questionText == "" {
			return nil, fmt.Errorf("bash HITL confirm answers.question is required")
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
		normalizedQuestion, err := normalizeHITLApprovalQuestion(definition, answerText, answerMap)
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

func normalizeHITLApprovalQuestion(definition map[string]any, answerText string, answerMap map[string]any) (map[string]any, error) {
	questionText := contracts.AnyStringNode(definition["question"])
	entry := map[string]any{
		"question": questionText,
		"header":   contracts.AnyStringNode(definition["header"]),
		"answer":   answerText,
	}

	if option, ok := hitlApprovalOptionByLabel(definition, answerText); ok {
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

	if option, ok := hitlApprovalOptionByValue(definition, answerText); ok {
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

type hitlApprovalOption struct {
	Label string
	Value string
}

func hitlApprovalOptionByLabel(definition map[string]any, label string) (hitlApprovalOption, bool) {
	for _, rawOption := range cloneAnySlice(definition["options"]) {
		option := contracts.AnyMapNode(rawOption)
		candidate := hitlApprovalOption{
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
	return hitlApprovalOption{}, false
}

func hitlApprovalOptionByValue(definition map[string]any, value string) (hitlApprovalOption, bool) {
	for _, rawOption := range cloneAnySlice(definition["options"]) {
		option := contracts.AnyMapNode(rawOption)
		candidate := hitlApprovalOption{
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
	return hitlApprovalOption{}, false
}
