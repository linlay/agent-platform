package llm

import (
	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"strings"
)

func structuredOrOutput(result contracts.ToolExecutionResult) any {
	if len(result.Structured) > 0 {
		return result.Structured
	}
	return result.Output
}

func sseResultValue(result contracts.ToolExecutionResult) any {
	if result.RawParams != nil {
		return result.RawParams
	}
	if result.Error != "" {
		return result.Output
	}
	return structuredOrOutput(result)
}

func formatToolErrorOutput(code string, message string) string {
	code = strings.TrimSpace(code)
	message = strings.TrimSpace(message)
	switch {
	case code == "":
		return message
	case message == "":
		return code
	default:
		return code + ": " + message
	}
}

func maxInt(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func cloneToolDefinition(def api.ToolDetailResponse) api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Key:           def.Key,
		Name:          def.Name,
		Label:         def.Label,
		Description:   def.Description,
		AfterCallHint: def.AfterCallHint,
		Parameters:    contracts.CloneMap(def.Parameters),
		Meta:          contracts.CloneMap(def.Meta),
	}
}
