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
		// An MCP error's Output also carries the server's supplemental content
		// for the model; the event keeps the platform error JSON parseable.
		if result.MCPResult != nil && len(result.Structured) > 0 {
			return contracts.MarshalJSON(result.Structured)
		}
		return result.Output
	}
	return structuredOrOutput(result)
}

// toolResultModelOutput returns the model text of an MCP result when the event
// result does not already carry it, e.g. supplemental content next to
// structuredContent or base64 elided from the model view.
func toolResultModelOutput(result contracts.ToolExecutionResult, sseResult any) *string {
	if result.MCPResult == nil {
		return nil
	}
	// The dispatcher wraps failed results in {output,error,exitCode}, so the
	// stored result never equals the model text even when sseResult does.
	if result.Error != "" || result.ExitCode != 0 {
		output := result.Output
		return &output
	}
	if text, ok := sseResult.(string); ok {
		if text == result.Output {
			return nil
		}
	} else if contracts.MarshalJSON(sseResult) == result.Output {
		return nil
	}
	output := result.Output
	return &output
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
