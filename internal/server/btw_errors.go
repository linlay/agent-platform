package server

func btwStatusError(status int, code string, message string) *statusError {
	return &statusError{
		Status:  status,
		Code:    code,
		Message: message,
		Data: map[string]any{
			"error": map[string]any{
				"code":    code,
				"message": message,
			},
		},
	}
}
