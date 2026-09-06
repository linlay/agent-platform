package memory

import "testing"

func TestNormalizeCategoryBusinessAliases(t *testing.T) {
	tests := map[string]string{
		"":              CategoryGeneral,
		"Preference":    CategoryPreference,
		"preferences":   CategoryPreference,
		"constraints":   CategoryConstraint,
		"profiles":      CategoryProfile,
		"runbook":       CategoryWorkflow,
		"decisions":     CategoryDecision,
		"terminology":   CategoryGlossary,
		"open-question": CategoryUnresolvedIssue,
		"blocked":       CategoryUnresolvedIssue,
	}

	for input, want := range tests {
		if got := normalizeCategory(input); got != want {
			t.Fatalf("normalizeCategory(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeCategoryPreservesCustomCategory(t *testing.T) {
	tests := map[string]string{
		"ops_checklist":    "ops_checklist",
		"memory.operation": "memory.operation",
		"project:alpha":    "project:alpha",
		"user_preference":  "user_preference",
	}

	for input, want := range tests {
		if got := normalizeCategory(input); got != want {
			t.Fatalf("normalizeCategory(%q) = %q, want %q", input, got, want)
		}
	}
}
