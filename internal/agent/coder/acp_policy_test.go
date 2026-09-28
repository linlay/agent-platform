package coder

import (
	"reflect"
	"testing"

	"agent-platform/internal/api"
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
		{name: "react", agentKey: "react", mode: "REACT", want: "native-only"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ModelOptionsFilterMode(tc.agentKey, tc.mode, tc.acpBridgeID); got != tc.want {
				t.Fatalf("ModelOptionsFilterMode()=%q want %q", got, tc.want)
			}
		})
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
	if got := ReasoningEffortOptions(true, nil); !reflect.DeepEqual(got, defaultACPReasoningEfforts) {
		t.Fatalf("ACP fallback reasoning efforts changed: %#v", got)
	}
	wantNative := []string{"NONE", "LOW", "MEDIUM", "HIGH", "XHIGH", "MAX"}
	gotNative := DefaultReasoningEffortOptions()
	if len(gotNative) != len(wantNative) {
		t.Fatalf("native reasoning effort count=%d want %d", len(gotNative), len(wantNative))
	}
	for index, effort := range wantNative {
		if gotNative[index].Key != effort {
			t.Fatalf("native reasoning effort[%d]=%q want %q", index, gotNative[index].Key, effort)
		}
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
