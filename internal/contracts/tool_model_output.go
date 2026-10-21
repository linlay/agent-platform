package contracts

import (
	"bytes"
	"encoding/json"
	"regexp"
	"unicode/utf8"
)

// ToolModelOutputMaxBytes bounds a tool's "content" text sent to the model.
// file_read pages to the same budget, measured as JSON-escaped bytes, so its
// content is never cut here.
const ToolModelOutputMaxBytes = 40000

var (
	contentBase64JSONPattern        = regexp.MustCompile(`"contentBase64"\s*:\s*"[^"]*"`)
	contentBase64EscapedJSONPattern = regexp.MustCompile(`\\"contentBase64\\"\s*:\s*\\"[^"]*\\"`)
)

// MarshalModelJSON encodes v as JSON without HTML escaping. Model-facing tool
// text never renders as HTML, and escaping <, > and & to <-style
// sequences only inflates code and markup.
func MarshalModelJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// ModelJSONStringLen returns the byte length of s inside a JSON string as
// MarshalModelJSON writes it, excluding the surrounding quotes.
func ModelJSONStringLen(s string) int {
	n := 0
	for index := 0; index < len(s); {
		b := s[index]
		if b < utf8.RuneSelf {
			switch {
			case b == '"' || b == '\\' || b == '\n' || b == '\r' || b == '\t':
				n += 2
			case b < 0x20:
				n += 6
			default:
				n++
			}
			index++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[index:])
		switch {
		case r == utf8.RuneError && size == 1:
			n += 6
		case r == ' ' || r == ' ':
			n += 6
		default:
			n += size
		}
		index += size
	}
	return n
}

// TruncateUTF8 cuts s to at most limit bytes without splitting a rune.
func TruncateUTF8(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// CompactToolModelOutput returns a safe, bounded JSON representation for the
// model without mutating the original structured result.
func CompactToolModelOutput(structured map[string]any, fallback string) string {
	if len(structured) == 0 {
		return fallback
	}
	data, err := MarshalModelJSON(CompactStructuredForModel(structured))
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
					out["contentBytes"] = len(content)
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
	if len(redacted) <= ToolModelOutputMaxBytes {
		return redacted, embeddedBase64Omitted, embeddedBase64Omitted
	}
	return TruncateUTF8(redacted, ToolModelOutputMaxBytes) +
		"\n...[content truncated for LLM]", true, embeddedBase64Omitted
}
