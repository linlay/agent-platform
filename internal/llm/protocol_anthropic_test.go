package llm

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	. "agent-platform/internal/contracts"
	"agent-platform/internal/models"
)

func TestAnthropicPrepareRequestUsesAdaptiveThinking(t *testing.T) {
	tests := []struct {
		name     string
		settings StageSettings
		effort   string
	}{
		{name: "disabled"},
		{name: "disabled with effort", settings: StageSettings{ReasoningEffort: "HIGH"}},
		{name: "default", settings: StageSettings{ReasoningEnabled: true}, effort: "medium"},
		{name: "low", settings: StageSettings{ReasoningEnabled: true, ReasoningEffort: "LOW"}, effort: "low"},
		{name: "medium", settings: StageSettings{ReasoningEnabled: true, ReasoningEffort: "MEDIUM"}, effort: "medium"},
		{name: "high", settings: StageSettings{ReasoningEnabled: true, ReasoningEffort: "HIGH"}, effort: "high"},
		{name: "xhigh", settings: StageSettings{ReasoningEnabled: true, ReasoningEffort: "XHIGH"}, effort: "xhigh"},
		{name: "max", settings: StageSettings{ReasoningEnabled: true, ReasoningEffort: "MAX"}, effort: "max"},
		{name: "normalized", settings: StageSettings{ReasoningEnabled: true, ReasoningEffort: " extra_high "}, effort: "xhigh"},
	}
	for _, modelID := range []string{"claude-haiku-5-5", "claude-opus-5-5"} {
		for _, tc := range tests {
			t.Run(modelID+"/"+tc.name, func(t *testing.T) {
				provider := models.ProviderDefinition{BaseURL: "https://example.com", APIKey: "token"}
				model := models.ModelDefinition{Protocol: "ANTHROPIC", ModelID: modelID}
				prepared, err := (&anthropicProtocol{}).PrepareRequest(protocolStreamParams{
					provider: provider, model: model, stageSettings: tc.settings,
					protocolConfig: resolveProtocolRuntimeConfig(provider, model),
					messages:       []openAIMessage{{Role: "user", Content: "hi"}},
				})
				if err != nil {
					t.Fatal(err)
				}
				var body map[string]any
				if err := json.Unmarshal(prepared.RequestBodyJSON, &body); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(body, prepared.RequestBody) {
					t.Fatalf("wire body and debug payload differ: %#v != %#v", body, prepared.RequestBody)
				}
				if tc.effort == "" {
					if body["thinking"] != nil || body["output_config"] != nil {
						t.Fatalf("unexpected explicit reasoning configuration: %#v", body)
					}
					return
				}
				if !reflect.DeepEqual(body["thinking"], map[string]any{"type": "adaptive"}) ||
					!reflect.DeepEqual(body["output_config"], map[string]any{"effort": tc.effort}) {
					t.Fatalf("unexpected reasoning configuration: %#v", body)
				}
			})
		}
	}
}

func TestAnthropicPrepareRequestOwnsThinkingAndEffort(t *testing.T) {
	outputConfig := map[string]any{"effort": "max", "format": map[string]any{"type": "json_schema"}}
	thinking := map[string]any{"type": "custom", "custom_option": true}
	for _, enabled := range []bool{true, false} {
		prepared, err := (&anthropicProtocol{}).PrepareRequest(protocolStreamParams{
			provider: models.ProviderDefinition{BaseURL: "https://example.com", APIKey: "token"},
			model:    models.ModelDefinition{Protocol: "ANTHROPIC", ModelID: "claude-opus-5-5"},
			stageSettings: StageSettings{
				ReasoningEnabled: enabled, ReasoningEffort: "HIGH",
			},
			messages: []openAIMessage{{Role: "user", Content: "hi"}},
			protocolConfig: protocolRuntimeConfig{Compat: map[string]any{
				"request": map[string]any{"always": map[string]any{
					"thinking": thinking, "output_config": outputConfig, "provider_option": true,
				}},
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		wantOutput := map[string]any{"format": map[string]any{"type": "json_schema"}}
		if enabled {
			wantOutput["effort"] = "high"
			if !reflect.DeepEqual(prepared.RequestBody["thinking"], map[string]any{"type": "adaptive"}) {
				t.Fatalf("compat changed thinking configuration: %#v", prepared.RequestBody)
			}
		} else if prepared.RequestBody["thinking"] != nil {
			t.Fatalf("compat added explicit thinking configuration: %#v", prepared.RequestBody)
		}
		if !reflect.DeepEqual(prepared.RequestBody["output_config"], wantOutput) || prepared.RequestBody["provider_option"] != true {
			t.Fatalf("unexpected compat configuration: %#v", prepared.RequestBody)
		}
		if outputConfig["effort"] != "max" || thinking["type"] != "custom" || len(thinking) != 2 {
			t.Fatalf("request preparation mutated shared compat: %#v, %#v", outputConfig, thinking)
		}
	}
}

func TestAnthropicPrepareRequestRejectsInvalidActiveEffort(t *testing.T) {
	for _, effort := range []string{"NONE", "INVALID"} {
		t.Run(effort, func(t *testing.T) {
			_, err := (&anthropicProtocol{}).PrepareRequest(protocolStreamParams{
				provider:      models.ProviderDefinition{BaseURL: "https://example.com", APIKey: "token"},
				model:         models.ModelDefinition{Protocol: "ANTHROPIC", ModelID: "claude-opus-5-5"},
				stageSettings: StageSettings{ReasoningEnabled: true, ReasoningEffort: effort},
				messages:      []openAIMessage{{Role: "user", Content: "hi"}},
			})
			if err == nil || !strings.Contains(err.Error(), "Anthropic reasoning effort") {
				t.Fatalf("expected invalid active effort to fail before sending the request, got %v", err)
			}
		})
	}
}

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
