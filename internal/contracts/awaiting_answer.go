package contracts

import (
	"fmt"
	"strings"
)

func AwaitingErrorAnswer(mode string, code string, message string) map[string]any {
	mode = normalizeAwaitingAnswerMode(mode)
	return map[string]any{
		"mode":   mode,
		"status": "error",
		"error": map[string]any{
			"code":    strings.TrimSpace(code),
			"message": strings.TrimSpace(message),
		},
	}
}

func AwaitingTimeoutAnswer(mode string, timeoutSeconds int64, elapsedSeconds int64) map[string]any {
	mode = normalizeAwaitingAnswerMode(mode)
	if timeoutSeconds < 0 {
		timeoutSeconds = 0
	}
	if elapsedSeconds <= 0 {
		elapsedSeconds = timeoutSeconds
	}
	message := fmt.Sprintf(
		"等待项已超时（%d秒）。原因：等待%s，超过配置的 %d 秒未收到有效提交。",
		timeoutSeconds,
		awaitingTimeoutActionLabel(mode),
		timeoutSeconds,
	)
	answer := AwaitingErrorAnswer(mode, "timeout", message)
	errPayload, _ := answer["error"].(map[string]any)
	errPayload["timeoutSeconds"] = timeoutSeconds
	errPayload["elapsedSeconds"] = elapsedSeconds
	errPayload["reason"] = "submit_not_received_before_timeout"
	return answer
}

func normalizeAwaitingAnswerMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "question"
	}
	return mode
}

func awaitingTimeoutActionLabel(mode string) string {
	switch normalizeAwaitingAnswerMode(mode) {
	case "question":
		return "问题回复"
	case "approval":
		return "审批确认"
	case "form":
		return "表单提交"
	case "plan":
		return "计划确认"
	default:
		return "等待项提交"
	}
}
