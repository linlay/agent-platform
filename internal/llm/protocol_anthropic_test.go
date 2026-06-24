package llm

import (
	"testing"

	. "agent-platform/internal/contracts"
)

func TestResolveAnthropicMaxTokensUsesStageMaxOutputTokens(t *testing.T) {
	got := resolveAnthropicMaxTokens(StageSettings{MaxOutputTokens: 8192})
	if got != 8192 {
		t.Fatalf("expected stage max output tokens 8192, got %d", got)
	}
}

func TestResolveAnthropicMaxTokensFallsBackToSourceDefault(t *testing.T) {
	got := resolveAnthropicMaxTokens(StageSettings{})
	if got != 4096 {
		t.Fatalf("expected default max output tokens 4096, got %d", got)
	}
}
