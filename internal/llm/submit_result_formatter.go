package llm

import (
	"encoding/json"
	"strings"

	"agent-platform-runner-go/internal/api"
	. "agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/frontendtools"
)

func formatSubmitResultForLLM(tool api.ToolDetailResponse, frontend *frontendtools.Registry, result ToolExecutionResult) string {
	format := strings.ToLower(strings.TrimSpace(AnyStringNode(tool.Meta["submitResultFormat"])))
	switch format {
	case "json-compact":
		return formatJSONCompactResult(result)
	case "", "summary", "kv", "qa":
		if frontend == nil {
			return result.Output
		}
		handler, ok := frontend.Handler(tool.Name)
		if !ok {
			return result.Output
		}
		if formatted, ok := handler.FormatSubmitResult(format, result); ok {
			return formatted
		}
		return result.Output
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
