package llm

import (
	"encoding/json"
	"fmt"
	"strings"

	. "agent-platform-runner-go/internal/contracts"
)

func formatSubmitResultForLLM(toolName string, meta map[string]any, result ToolExecutionResult) string {
	format := strings.ToLower(strings.TrimSpace(AnyStringNode(meta["submitResultFormat"])))
	switch format {
	case "summary":
		return formatSummaryResult(toolName, result)
	case "kv":
		return formatKVResult(toolName, result)
	case "json-compact":
		return formatJSONCompactResult(result)
	default:
		return result.Output
	}
}

func formatSummaryResult(toolName string, result ToolExecutionResult) string {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "_ask_user_question_":
		return formatQuestionSummary(result)
	case "_ask_user_approval_":
		return formatApprovalSummary(result)
	default:
		return result.Output
	}
}

func formatKVResult(toolName string, result ToolExecutionResult) string {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "_ask_user_question_":
		return formatQuestionKV(result)
	case "_ask_user_approval_":
		return formatApprovalKV(result)
	default:
		return result.Output
	}
}

func formatJSONCompactResult(result ToolExecutionResult) string {
	if len(result.Structured) == 0 {
		return result.Output
	}
	data, err := json.Marshal(result.Structured)
	if err != nil {
		return result.Output
	}
	return string(data)
}

func formatQuestionSummary(result ToolExecutionResult) string {
	answers, ok := structuredAnswers(result)
	if !ok {
		return result.Output
	}
	if len(answers) == 0 {
		return result.Output
	}
	lines := make([]string, 0, len(answers)+1)
	lines = append(lines, "用户回答了以下问题:")
	for _, answer := range answers {
		key := formatAnswerKey(answer)
		if key == "" {
			return result.Output
		}
		lines = append(lines, "- "+key+": "+formatAnswerValue(answer["answer"]))
	}
	return strings.Join(lines, "\n")
}

func formatQuestionKV(result ToolExecutionResult) string {
	answers, ok := structuredAnswers(result)
	if !ok {
		return result.Output
	}
	if len(answers) == 0 {
		return result.Output
	}
	items := make([]string, 0, len(answers))
	for _, answer := range answers {
		key := formatAnswerKey(answer)
		if key == "" {
			return result.Output
		}
		items = append(items, key+"="+formatAnswerValue(answer["answer"]))
	}
	return strings.Join(items, "; ")
}

func formatApprovalSummary(result ToolExecutionResult) string {
	if value := strings.TrimSpace(AnyStringNode(result.Structured["value"])); value != "" {
		return "用户选择了: " + value
	}
	if freeText := strings.TrimSpace(AnyStringNode(result.Structured["freeText"])); freeText != "" {
		return "用户自由输入: " + freeText
	}
	return result.Output
}

func formatApprovalKV(result ToolExecutionResult) string {
	if value := strings.TrimSpace(AnyStringNode(result.Structured["value"])); value != "" {
		return "选择=" + value
	}
	if freeText := strings.TrimSpace(AnyStringNode(result.Structured["freeText"])); freeText != "" {
		return "自由输入=" + freeText
	}
	return result.Output
}

func formatAnswerKey(answer map[string]any) string {
	if header := strings.TrimSpace(AnyStringNode(answer["header"])); header != "" {
		return header
	}
	return strings.TrimSpace(AnyStringNode(answer["question"]))
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

func structuredAnswers(result ToolExecutionResult) ([]map[string]any, bool) {
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
		answer := AnyMapNode(rawAnswer)
		if len(answer) == 0 {
			return nil, false
		}
		answers = append(answers, answer)
	}
	return answers, true
}
