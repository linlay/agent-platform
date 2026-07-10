package coder

import (
	"reflect"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/models"
)

func TestModelOptionsFilterModeKeepsACPScopedToCoder(t *testing.T) {
	tests := []struct {
		name        string
		agentKey    string
		mode        string
		acpBridgeID string
		want        string
	}{
		{name: "empty agent key", mode: "CODER", acpBridgeID: "codex", want: "native-only"},
		{name: "native coder", agentKey: "coder", mode: "CODER", want: "native-only"},
		{name: "acp coder", agentKey: "coder", mode: "CODER", acpBridgeID: "codex", want: "acp-only"},
		{name: "ordinary proxy", agentKey: "proxy", mode: "PROXY", acpBridgeID: "codex", want: ""},
		{name: "react", agentKey: "react", mode: "REACT", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ModelOptionsFilterMode(tc.agentKey, tc.mode, tc.acpBridgeID); got != tc.want {
				t.Fatalf("ModelOptionsFilterMode()=%q want %q", got, tc.want)
			}
		})
	}
}

func TestDefaultModelOptionKeyPrefersVisibleNormalFallback(t *testing.T) {
	options := []api.CoderModelOption{
		{Key: "acp", Protocol: models.ProtocolACPPassthrough},
		{Key: "native-a"},
		{Key: "native-b"},
	}
	if got := DefaultModelOptionKey(options, "native-b", "native-a"); got != "native-b" {
		t.Fatalf("expected preferred visible key, got %q", got)
	}
	if got := DefaultModelOptionKey(options, "missing", "native-a"); got != "native-a" {
		t.Fatalf("expected visible configured default key, got %q", got)
	}
	if got := DefaultModelOptionKey(options, "missing", "also-missing"); got != "native-a" {
		t.Fatalf("expected first native fallback, got %q", got)
	}
	if got := DefaultModelOptionKey([]api.CoderModelOption{{Key: "acp", Protocol: models.ProtocolACPPassthrough}}, "", ""); got != "acp" {
		t.Fatalf("expected ACP fallback when only ACP model is visible, got %q", got)
	}
}

func TestModelConfigFromOptionsAndReasoningEffort(t *testing.T) {
	cfg := ModelConfigFromOptions(api.CoderModelOptionsResponse{
		Models:                 []api.CoderModelOption{{Key: "fallback-model"}},
		DefaultModelKey:        " coder-model ",
		DefaultReasoningEffort: "NONE",
		DefaultServiceTier:     "FAST",
	})
	if cfg["modelKey"] != "coder-model" || cfg["serviceTier"] != "FAST" {
		t.Fatalf("unexpected model config: %#v", cfg)
	}
	reasoning, _ := cfg["reasoning"].(map[string]any)
	if enabled, ok := reasoning["enabled"].(bool); !ok || enabled {
		t.Fatalf("expected NONE reasoning to disable reasoning, got %#v", cfg)
	}
	if got := ModelConfigReasoningEffort(cfg); got != "NONE" {
		t.Fatalf("ModelConfigReasoningEffort()=%q want NONE", got)
	}

	fallback := ModelConfigFromOptions(api.CoderModelOptionsResponse{
		Models:                 []api.CoderModelOption{{Key: "fallback-model"}},
		DefaultReasoningEffort: "HIGH",
	})
	if fallback["modelKey"] != "fallback-model" {
		t.Fatalf("expected first model fallback, got %#v", fallback)
	}
	if got := ModelConfigReasoningEffort(fallback); got != "HIGH" {
		t.Fatalf("ModelConfigReasoningEffort()=%q want HIGH", got)
	}
	if got := ModelConfigFromOptions(api.CoderModelOptionsResponse{}); got != nil {
		t.Fatalf("expected nil config without a model key, got %#v", got)
	}
}

func TestReasoningEffortOptionsAndACPModelAllowance(t *testing.T) {
	modelOptions := []api.CoderModelOption{
		{Key: "alpha", ReasoningEfforts: []string{"HIGH", "extra_high", "bad"}},
		{Key: "beta", ReasoningEfforts: []string{"LOW"}},
	}
	want := []api.ReasoningEffortOption{
		{Key: "NONE", Label: "NONE"},
		{Key: "LOW", Label: "LOW"},
		{Key: "HIGH", Label: "HIGH"},
		{Key: "XHIGH", Label: "XHIGH"},
	}
	if got := ReasoningEffortOptions(true, modelOptions); !reflect.DeepEqual(got, want) {
		t.Fatalf("ReasoningEffortOptions()=%#v want %#v", got, want)
	}
	if got := ReasoningEffortOptions(false, modelOptions); !reflect.DeepEqual(got, DefaultReasoningEffortOptions()) {
		t.Fatalf("non-ACP reasoning efforts should use defaults, got %#v", got)
	}
	if !ReasoningEffortAllowedForACPModel("HIGH", "alpha", modelOptions) {
		t.Fatalf("expected HIGH to be allowed for alpha")
	}
	if ReasoningEffortAllowedForACPModel("LOW", "alpha", modelOptions) {
		t.Fatalf("did not expect LOW to be allowed for alpha")
	}
	if ReasoningEffortAllowedForACPModel("MEDIUM", "", modelOptions) {
		t.Fatalf("declared ACP model efforts should reject undeclared MEDIUM")
	}
	if !ReasoningEffortAllowedForACPModel("NONE", "alpha", modelOptions) {
		t.Fatalf("NONE reasoning should always be accepted")
	}
	if ReasoningEffortAllowedForACPModel("bad", "alpha", modelOptions) {
		t.Fatalf("invalid reasoning effort should be rejected")
	}
}

func TestServiceTierOptionsAndACPModelAllowance(t *testing.T) {
	modelOptions := []api.CoderModelOption{
		{Key: "alpha", ServiceTiers: []string{"fast", "flex", "auto"}},
		{Key: "beta", ServiceTiers: []string{"premium"}},
	}
	want := []api.ServiceTierOption{
		{Key: "STANDARD", Label: "Standard"},
		{Key: "FAST", Label: "Fast"},
		{Key: "FLEX", Label: "Flex"},
		{Key: "PREMIUM", Label: "PREMIUM"},
	}
	if got := ServiceTierOptions(true, modelOptions); !reflect.DeepEqual(got, want) {
		t.Fatalf("ServiceTierOptions()=%#v want %#v", got, want)
	}
	if got := ServiceTierOptions(false, modelOptions); !reflect.DeepEqual(got, []api.ServiceTierOption{{Key: "STANDARD", Label: "Standard"}}) {
		t.Fatalf("non-ACP service tiers should use standard only, got %#v", got)
	}
	if got := DefaultServiceTier(true, "fast", want); got != "FAST" {
		t.Fatalf("DefaultServiceTier()=%q want FAST", got)
	}
	if got := DefaultServiceTier(true, "auto", want); got != "" {
		t.Fatalf("auto/default service tier should normalize to empty, got %q", got)
	}
	if got := DefaultServiceTier(true, "turbo", want); got != "" {
		t.Fatalf("unknown configured service tier should be ignored, got %q", got)
	}
	if !ServiceTierAllowedForACPModel("FAST", "alpha", modelOptions) {
		t.Fatalf("expected FAST to be allowed for alpha")
	}
	if ServiceTierAllowedForACPModel("PREMIUM", "alpha", modelOptions) {
		t.Fatalf("did not expect PREMIUM to be allowed for alpha")
	}
	if !ServiceTierAllowedForACPModel("PREMIUM", "", modelOptions) {
		t.Fatalf("expected PREMIUM to be allowed when at least one ACP model supports it")
	}
	if !ServiceTierAllowedForACPModel("", "alpha", modelOptions) {
		t.Fatalf("empty service tier should be allowed")
	}
	if ServiceTierAllowedForACPModel("FAST", "missing", modelOptions) {
		t.Fatalf("missing model should not allow non-empty service tier")
	}
}
