package observability

func LogToolInvocation(toolName string, status string, fields map[string]any) {
	payload := map[string]any{
		"toolName": toolName,
		"status":   status,
	}
	for key, value := range fields {
		payload[key] = value
	}
	Log("tool.invoke", payload)
}
