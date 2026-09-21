package observability

import (
	"encoding/json"
	"log"
	"regexp"
	"strings"
)

const HiddenToken = "<HIDDEN_TOKEN>"

var sensitivePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(bearer\s+)[^\s,;"'<>]+`),
	regexp.MustCompile(`(?i)((?:["']|\b)(?:api[_-]?key|x[_-]?api[_-]?key|access[_-]?token|refresh[_-]?token|id[_-]?token|client[_-]?secret|token|secret|password|passwd|authorization|cookie)["']?\s*[:=]\s*)(?:"(?:\\.|[^"\\])*"|'[^']*'|[^\s,;&}\]<>]+)`),
	regexp.MustCompile(`(?i)((?:[?&](?:api[_-]?key|access[_-]?token|refresh[_-]?token|token|secret|password)=))[^&\s]+`),
	regexp.MustCompile(`(?i)\bsk-[a-z0-9_-]+`),
}

func Log(category string, fields map[string]any) {
	payload := map[string]any{"category": category}
	for key, value := range fields {
		payload[key] = value
	}
	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[obs][%s] marshal_error=%v", category, err)
		return
	}
	log.Printf("%s", data)
}

func SanitizeLog(text string) string {
	sanitized := text
	sanitized = sensitivePatterns[0].ReplaceAllString(sanitized, "${1}"+HiddenToken)
	sanitized = sensitivePatterns[1].ReplaceAllStringFunc(sanitized, func(match string) string {
		parts := sensitivePatterns[1].FindStringSubmatch(match)
		prefix := parts[1]
		value := strings.TrimPrefix(match, prefix)
		if strings.HasPrefix(value, "\"") {
			return prefix + "\"" + HiddenToken + "\""
		}
		if strings.HasPrefix(value, "'") {
			return prefix + "'" + HiddenToken + "'"
		}
		return prefix + HiddenToken
	})
	sanitized = sensitivePatterns[2].ReplaceAllString(sanitized, "${1}"+HiddenToken)
	sanitized = sensitivePatterns[3].ReplaceAllString(sanitized, HiddenToken)
	return sanitized
}
