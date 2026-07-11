package coder

import (
	"strings"
	"testing"

	"agent-platform/internal/api"
)

func TestResolveACPBridgeAppliesDefaultsAndNormalizesValues(t *testing.T) {
	got, err := ResolveACPBridge(" codex ", func(bridgeID string) (ACPBridgeConfig, bool) {
		if bridgeID != "codex" {
			t.Fatalf("lookup bridgeID=%q", bridgeID)
		}
		return ACPBridgeConfig{BaseURL: " ws://bridge ", AuthToken: " token "}, true
	})
	if err != nil {
		t.Fatalf("ResolveACPBridge: %v", err)
	}
	if got.BaseURL != "ws://bridge" || got.Transport != "ws" || got.Token != "token" || got.TimeoutMS != DefaultACPBridgeTimeoutMS || got.Timeout != 300 {
		t.Fatalf("unexpected routing config %#v", got)
	}
}

func TestResolveACPBridgePreservesConfigurationErrors(t *testing.T) {
	if _, err := ResolveACPBridge("", nil); err == nil || !strings.Contains(err.Error(), "acpBridgeId is required") {
		t.Fatalf("expected missing id error, got %v", err)
	}
	if _, err := ResolveACPBridge("missing", func(string) (ACPBridgeConfig, bool) { return ACPBridgeConfig{}, false }); err == nil || !strings.Contains(err.Error(), `ACP bridge "missing" is not configured`) {
		t.Fatalf("expected missing bridge error, got %v", err)
	}
	if _, err := ResolveACPBridge("empty", func(string) (ACPBridgeConfig, bool) { return ACPBridgeConfig{}, true }); err == nil || !strings.Contains(err.Error(), "is missing base-url") {
		t.Fatalf("expected missing base-url error, got %v", err)
	}
}

func TestResolveACPModelOptionsUsesRequestOverrideAndRuntimeModelID(t *testing.T) {
	got := ResolveACPModelOptions(Mode, "session-model", &api.QueryModelOptions{
		Key:             "request-model",
		ReasoningEffort: " HIGH ",
		ServiceTier:     " FAST ",
	}, func(modelKey string) string {
		if modelKey != "request-model" {
			t.Fatalf("resolver modelKey=%q", modelKey)
		}
		return "provider-model"
	})
	if got.Key != "request-model" || got.ModelID != "provider-model" || got.ReasoningEffort != "HIGH" || got.ServiceTier != "FAST" {
		t.Fatalf("unexpected model options %#v", got)
	}
	if got := ResolveACPModelOptions("REACT", "session-model", nil, nil); got != nil {
		t.Fatalf("non-CODER model options must pass through, got %#v", got)
	}
}

func TestResolveACPModelOptionsFallsBackToKeyAndKeepsMetadataOnly(t *testing.T) {
	got := ResolveACPModelOptions(Mode, "model-key", nil, func(string) string { return "" })
	if got.Key != "model-key" || got.ModelID != "model-key" {
		t.Fatalf("expected key fallback, got %#v", got)
	}
	metadataOnly := ResolveACPModelOptions(Mode, "", &api.QueryModelOptions{ReasoningEffort: "LOW"}, nil)
	if metadataOnly.Key != "" || metadataOnly.ModelID != "" || metadataOnly.ReasoningEffort != "LOW" {
		t.Fatalf("unexpected metadata-only options %#v", metadataOnly)
	}
}
