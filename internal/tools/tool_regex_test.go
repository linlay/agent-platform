package tools

import (
	"testing"

	"agent-platform/internal/contracts"
)

func TestInvokeRegexCountMatchesAllOccurrences(t *testing.T) {
	result := (&RuntimeToolExecutor{}).invokeRegex(map[string]any{
		"operation": "count",
		"text":      "one needle, two needle, three",
		"pattern":   "needle",
	})
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected regex success, got %#v", result)
	}
	if got := result.Structured["count"]; got != 2 {
		t.Fatalf("count = %#v, want 2", got)
	}
	if got := len(regexMatchesResult(t, result)); got != 2 {
		t.Fatalf("preview count = %d, want 2", got)
	}
}

func TestInvokeRegexMatchesReturnsOffsetsAndGroups(t *testing.T) {
	result := (&RuntimeToolExecutor{}).invokeRegex(map[string]any{
		"operation": "matches",
		"text":      "id=abc-123 id=xyz-987",
		"pattern":   `id=([a-z]+)-(\d+)`,
	})
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected regex success, got %#v", result)
	}
	matches := regexMatchesResult(t, result)
	if len(matches) != 2 {
		t.Fatalf("matches length = %d, want 2", len(matches))
	}
	first := matches[0]
	if first["text"] != "id=abc-123" || first["start"] != 0 || first["end"] != 10 {
		t.Fatalf("unexpected first match: %#v", first)
	}
	groups, ok := first["groups"].([]any)
	if !ok {
		t.Fatalf("groups = %#v, want []any", first["groups"])
	}
	if len(groups) != 2 || groups[0] != "abc" || groups[1] != "123" {
		t.Fatalf("unexpected groups: %#v", groups)
	}
}

func TestInvokeRegexLimitTruncatesPreviewOnly(t *testing.T) {
	result := (&RuntimeToolExecutor{}).invokeRegex(map[string]any{
		"operation": "matches",
		"text":      "a a a",
		"pattern":   "a",
		"limit":     float64(1),
	})
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected regex success, got %#v", result)
	}
	if got := result.Structured["count"]; got != 3 {
		t.Fatalf("count = %#v, want 3", got)
	}
	if got := len(regexMatchesResult(t, result)); got != 1 {
		t.Fatalf("preview count = %d, want 1", got)
	}
	if result.Structured["truncated"] != true {
		t.Fatalf("expected truncated=true, got %#v", result.Structured)
	}
}

func TestInvokeRegexCaseInsensitive(t *testing.T) {
	result := (&RuntimeToolExecutor{}).invokeRegex(map[string]any{
		"operation":       "count",
		"text":            "Alpha alpha ALPHA",
		"pattern":         "alpha",
		"caseInsensitive": true,
	})
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected regex success, got %#v", result)
	}
	if got := result.Structured["count"]; got != 3 {
		t.Fatalf("count = %#v, want 3", got)
	}
}

func TestInvokeRegexInvalidPattern(t *testing.T) {
	result := (&RuntimeToolExecutor{}).invokeRegex(map[string]any{
		"operation": "count",
		"text":      "anything",
		"pattern":   "[",
	})
	if result.ExitCode == 0 || result.Error != "regex_invalid_pattern" {
		t.Fatalf("expected invalid pattern error, got %#v", result)
	}
	if result.Structured["error"] != "regex_invalid_pattern" || result.Structured["message"] == "" {
		t.Fatalf("unexpected structured error: %#v", result.Structured)
	}
}

func TestInvokeRegexNoMatchReturnsEmptySuccess(t *testing.T) {
	result := (&RuntimeToolExecutor{}).invokeRegex(map[string]any{
		"operation": "matches",
		"text":      "abc",
		"pattern":   "xyz",
	})
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected regex success, got %#v", result)
	}
	if got := result.Structured["count"]; got != 0 {
		t.Fatalf("count = %#v, want 0", got)
	}
	if matches := regexMatchesResult(t, result); len(matches) != 0 {
		t.Fatalf("expected empty matches, got %#v", matches)
	}
}

func regexMatchesResult(t *testing.T, result contracts.ToolExecutionResult) []map[string]any {
	t.Helper()
	matches, ok := result.Structured["matches"].([]map[string]any)
	if !ok {
		t.Fatalf("matches = %#v, want []map[string]any", result.Structured["matches"])
	}
	return matches
}
