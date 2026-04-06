package observability

import (
	"encoding/json"
	"log"
	"regexp"
)

var sensitivePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)bearer\s+[a-z0-9_\-\.=]+`),
	regexp.MustCompile(`(?i)(api[_-]?key|token|secret)\s*[:=]\s*[a-z0-9_\-\.=]+`),
	regexp.MustCompile(`(?i)sk-[a-z0-9]+`),
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
	for _, pattern := range sensitivePatterns {
		sanitized = pattern.ReplaceAllString(sanitized, "[redacted]")
	}
	return sanitized
}
