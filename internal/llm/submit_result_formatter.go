package llm

import (
	"encoding/json"
	"regexp"
	"strings"

	"agent-platform/internal/api"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/frontendtools"
)

const llmStructuredContentMaxChars = 20000

var (
	contentBase64JSONRe        = regexp.MustCompile(`"contentBase64"\s*:\s*"[^"]*"`)
	contentBase64EscapedJSONRe = regexp.MustCompile(`\\"contentBase64\\"\s*:\s*\\"[^"]*\\"`)
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
	data, err := json.Marshal(compactStructuredResultForLLM(result.Structured))
	if err != nil {
		return result.Output
	}
	return string(data)
}

func compactStructuredResultForLLM(structured map[string]any) map[string]any {
	out := make(map[string]any, len(structured)+2)
	for key, value := range structured {
		if key == "contentBase64" {
			if encoded, ok := value.(string); ok && encoded != "" {
				out["contentBase64Omitted"] = true
				out["contentBase64Chars"] = len(encoded)
			}
			continue
		}
		if key == "content" {
			if content, ok := value.(string); ok {
				compactContent, changed, embeddedBase64Omitted := compactTextContentForLLM(content)
				out[key] = compactContent
				if changed {
					out["contentTruncatedForLLM"] = true
					out["contentChars"] = len(content)
				}
				if embeddedBase64Omitted {
					out["embeddedBase64Omitted"] = true
				}
				continue
			}
		}
		out[key] = value
	}
	return out
}

func compactTextContentForLLM(content string) (string, bool, bool) {
	redacted := contentBase64JSONRe.ReplaceAllString(content, `"contentBase64":"<omitted>"`)
	redacted = contentBase64EscapedJSONRe.ReplaceAllString(redacted, `\"contentBase64\":\"<omitted>\"`)
	embeddedBase64Omitted := redacted != content
	changed := embeddedBase64Omitted
	if len(redacted) <= llmStructuredContentMaxChars {
		return redacted, changed, embeddedBase64Omitted
	}
	return redacted[:llmStructuredContentMaxChars] +
		"\n...[content truncated for LLM; use file_read with offset/limit for narrower ranges]", true, embeddedBase64Omitted
}
