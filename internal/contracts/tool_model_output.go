package contracts

import (
	"encoding/json"
	"regexp"
)

const ToolModelOutputMaxChars = 20000

var (
	contentBase64JSONPattern        = regexp.MustCompile(`"contentBase64"\s*:\s*"[^"]*"`)
	contentBase64EscapedJSONPattern = regexp.MustCompile(`\\"contentBase64\\"\s*:\s*\\"[^"]*\\"`)
)

// CompactToolModelOutput returns a safe, bounded JSON representation for the
// model without mutating the original structured result.
func CompactToolModelOutput(structured map[string]any, fallback string) string {
	if len(structured) == 0 {
		return fallback
	}
	data, err := json.Marshal(CompactStructuredForModel(structured))
	if err != nil {
		return fallback
	}
	return string(data)
}

// CompactStructuredForModel clones the root object while removing large binary
// payloads and bounding text that is sent back to a model.
func CompactStructuredForModel(structured map[string]any) map[string]any {
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
				compact, changed, embeddedBase64Omitted := compactToolTextForModel(content)
				out[key] = compact
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

func compactToolTextForModel(content string) (string, bool, bool) {
	redacted := contentBase64JSONPattern.ReplaceAllString(content, `"contentBase64":"<omitted>"`)
	redacted = contentBase64EscapedJSONPattern.ReplaceAllString(redacted, `\"contentBase64\":\"<omitted>\"`)
	embeddedBase64Omitted := redacted != content
	if len(redacted) <= ToolModelOutputMaxChars {
		return redacted, embeddedBase64Omitted, embeddedBase64Omitted
	}
	return redacted[:ToolModelOutputMaxChars] +
		"\n...[content truncated for LLM; use file_read with offset/limit for narrower ranges]", true, embeddedBase64Omitted
}
