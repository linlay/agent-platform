package memory

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"agent-platform-runner-go/internal/api"
)

var memorySecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)api[_-]?key\s*=\s*[^\s]+`),
	regexp.MustCompile(`(?i)secret\s*=\s*[^\s]+`),
	regexp.MustCompile(`(?i)password\s*=\s*[^\s]+`),
}

var memoryInjectionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)ignore previous instructions`),
	regexp.MustCompile(`(?i)reveal the system prompt`),
	regexp.MustCompile(`(?i)system prompt`),
}

const memoryInvisibleUnicodeChars = "\u200B\u200C\u200D\uFEFF"

var (
	ErrMemoryPromptInjection = errors.New("memory content rejected: prompt injection pattern detected")
	ErrMemorySecretLeak      = errors.New("memory content rejected: secret-like content detected")
	ErrMemoryInvalidUnicode  = errors.New("memory content rejected: invisible unicode detected")
)

type MemorySafetyError struct {
	Reason string
	Cause  error
}

func (e *MemorySafetyError) Error() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.Reason) == "" {
		return e.Cause.Error()
	}
	return fmt.Sprintf("%s: %s", e.Cause.Error(), strings.TrimSpace(e.Reason))
}

func (e *MemorySafetyError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func IsMemorySafetyError(err error) bool {
	var target *MemorySafetyError
	return errors.As(err, &target)
}

func NormalizeMemoryText(text string) string {
	text = strings.Map(func(r rune) rune {
		if strings.ContainsRune(memoryInvisibleUnicodeChars, r) {
			return -1
		}
		return r
	}, text)
	return strings.TrimSpace(text)
}

func ValidateMemoryText(text string) error {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}
	if strings.ContainsAny(text, memoryInvisibleUnicodeChars) {
		return &MemorySafetyError{Reason: "invisible unicode is not allowed", Cause: ErrMemoryInvalidUnicode}
	}
	for _, pattern := range memoryInjectionPatterns {
		if pattern.MatchString(trimmed) {
			return &MemorySafetyError{Reason: "memory content must not contain prompt-injection directives", Cause: ErrMemoryPromptInjection}
		}
	}
	for _, pattern := range memorySecretPatterns {
		if pattern.MatchString(trimmed) {
			return &MemorySafetyError{Reason: "memory content must not contain secrets or credentials", Cause: ErrMemorySecretLeak}
		}
	}
	return nil
}

func validateStoredMemoryItem(item api.StoredMemoryResponse) error {
	for _, candidate := range []string{item.Title, item.Summary} {
		if err := ValidateMemoryText(candidate); err != nil {
			return err
		}
	}
	return nil
}

func sanitizeMemoryText(text string) string {
	text = NormalizeMemoryText(text)
	replacements := []string{
		"Ignore previous instructions", "[filtered: prompt injection]",
		"ignore previous instructions", "[filtered: prompt injection]",
		"reveal the system prompt", "[filtered: prompt injection]",
		"system prompt", "[filtered: protected content]",
	}
	replacer := strings.NewReplacer(replacements...)
	text = replacer.Replace(text)
	for _, pattern := range memorySecretPatterns {
		text = pattern.ReplaceAllString(text, "[filtered: secret]")
	}
	return strings.TrimSpace(text)
}
