package frontendtools

import (
	"encoding/json"
	"fmt"
	"strings"

	"agent-platform-runner-go/internal/contracts"
)

func normalizeQuestionAnswer(definition map[string]any, rawAnswer any) (any, error) {
	questionType := strings.ToLower(contracts.AnyStringNode(definition["type"]))
	switch questionType {
	case "number":
		switch value := rawAnswer.(type) {
		case int, int32, int64, float32, float64, json.Number:
			return value, nil
		default:
			return nil, fmt.Errorf("answer must be a number")
		}
	case "text", "password":
		text := contracts.AnyStringNode(rawAnswer)
		if text == "" {
			return nil, fmt.Errorf("answer must be a non-empty string")
		}
		return text, nil
	case "select":
		if contracts.AnyBoolNode(definition["multiSelect"]) {
			items, ok := rawAnswer.([]any)
			if !ok {
				return nil, fmt.Errorf("answer must be an array")
			}
			values := make([]string, 0, len(items))
			for _, item := range items {
				text := contracts.AnyStringNode(item)
				if text == "" {
					return nil, fmt.Errorf("answer items must be non-empty strings")
				}
				values = append(values, text)
			}
			if len(values) == 0 {
				return nil, fmt.Errorf("answer must not be empty")
			}
			if !contracts.AnyBoolNode(definition["allowFreeText"]) {
				allowed := allowedQuestionOptions(definition)
				for _, value := range values {
					if !allowed[value] {
						return nil, fmt.Errorf("answer item %q is not an allowed option", value)
					}
				}
			}
			return values, nil
		}
		if items, ok := rawAnswer.([]any); ok && len(items) == 1 {
			rawAnswer = items[0]
		}
		text := contracts.AnyStringNode(rawAnswer)
		if text == "" {
			return nil, fmt.Errorf("answer must be a non-empty string")
		}
		if !contracts.AnyBoolNode(definition["allowFreeText"]) {
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
		option := contracts.AnyMapNode(rawOption)
		label := contracts.AnyStringNode(option["label"])
		if label != "" {
			allowed[label] = true
		}
	}
	return allowed
}

func structuredAnswers(result contracts.ToolExecutionResult) ([]map[string]any, bool) {
	answers, ok := result.Structured["answers"].([]map[string]any)
	if ok {
		return answers, true
	}
	rawAnswers, ok := result.Structured["answers"].([]any)
	if !ok {
		return nil, false
	}
	answers = make([]map[string]any, 0, len(rawAnswers))
	for _, rawAnswer := range rawAnswers {
		answer := contracts.AnyMapNode(rawAnswer)
		if len(answer) == 0 {
			return nil, false
		}
		answers = append(answers, answer)
	}
	return answers, true
}

func formatAnswerKey(answer map[string]any) string {
	if header := strings.TrimSpace(contracts.AnyStringNode(answer["header"])); header != "" {
		return header
	}
	return strings.TrimSpace(contracts.AnyStringNode(answer["question"]))
}

func formatAnswerValue(value any) string {
	switch typed := value.(type) {
	case []string:
		return strings.Join(typed, ", ")
	case []any:
		items := make([]string, 0, len(typed))
		for _, item := range typed {
			items = append(items, strings.TrimSpace(fmt.Sprint(item)))
		}
		return strings.Join(items, ", ")
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func cloneAwaitQuestions(input []any) []any {
	if len(input) == 0 {
		return nil
	}
	cloned := make([]any, 0, len(input))
	for _, value := range input {
		cloned = append(cloned, deepCloneAny(value))
	}
	return cloned
}

func deepCloneAny(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		cloned := make(map[string]any, len(typed))
		for key, item := range typed {
			cloned[key] = deepCloneAny(item)
		}
		return cloned
	case []any:
		return cloneAwaitQuestions(typed)
	default:
		return typed
	}
}

func sanitizeQuestionFields(questions []any) []any {
	for _, raw := range questions {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		questionType := strings.ToLower(strings.TrimSpace(contracts.AnyStringNode(item["type"])))
		if questionType == "select" {
			continue
		}
		delete(item, "allowFreeText")
		delete(item, "freeTextPlaceholder")
		delete(item, "multiSelect")
		delete(item, "options")
	}
	return questions
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
