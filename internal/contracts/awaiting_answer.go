package contracts

import "strings"

func AwaitingErrorAnswer(mode string, code string, message string) map[string]any {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "question"
	}
	return map[string]any{
		"mode":   mode,
		"status": "error",
		"error": map[string]any{
			"code":    strings.TrimSpace(code),
			"message": strings.TrimSpace(message),
		},
	}
}
