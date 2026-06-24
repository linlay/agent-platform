package llm

import (
	"testing"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

func TestResolveAnthropicMaxTokensUsesStageMaxOutputTokens(t *testing.T) {
	cfg := config.Config{
		Anthropic: config.AnthropicConfig{MaxOutputTokens: 4096},
	}

	got := resolveAnthropicMaxTokens(cfg, StageSettings{MaxOutputTokens: 8192})
	if got != 8192 {
		t.Fatalf("expected stage max output tokens 8192, got %d", got)
	}
}

func TestResolveAnthropicMaxTokensFallsBackToAnthropicMaxOutputTokens(t *testing.T) {
	cfg := config.Config{
		Anthropic: config.AnthropicConfig{MaxOutputTokens: 4096},
	}

	got := resolveAnthropicMaxTokens(cfg, StageSettings{})
	if got != 4096 {
		t.Fatalf("expected default max output tokens 4096, got %d", got)
	}
}

func TestResolveAnthropicMaxTokensFallsBackToLiteralDefault(t *testing.T) {
	got := resolveAnthropicMaxTokens(config.Config{}, StageSettings{})
	if got != 4096 {
		t.Fatalf("expected literal default max output tokens 4096, got %d", got)
	}
}
