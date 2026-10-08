package llm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
)

func TestAnthropicModelYAMLDeterminesRequestOutputLimit(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "models"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "providers"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "providers", "babelark.yml"), []byte("key: babelark\nbaseUrl: https://example.com\napiKey: test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"haiku", "sonnet", "opus"} {
		key := "babelark-claude-" + line + "-5_5"
		yaml := "key: " + key + "\nprovider: babelark\nprotocol: ANTHROPIC\nmodelId: claude-" + line + "-5-5\nmaxInputTokens: 1000000\nmaxOutputTokens: 128000\n"
		if err := os.WriteFile(filepath.Join(root, "models", key+".yml"), []byte(yaml), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	registry, err := models.LoadModelRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"haiku", "sonnet", "opus"} {
		t.Run(line, func(t *testing.T) {
			model, provider, err := registry.Get("babelark-claude-" + line + "-5_5")
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := (&anthropicProtocol{}).PrepareRequest(protocolStreamParams{
				provider: provider, model: model,
				stageSettings:  contracts.StageSettings{ReasoningEnabled: true, ReasoningEffort: "HIGH"},
				protocolConfig: resolveProtocolRuntimeConfig(provider, model),
				messages:       []openAIMessage{{Role: "user", Content: "hi"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if prepared.RequestBody["max_tokens"] != float64(128000) {
				t.Fatalf("model YAML was ignored: max_tokens = %#v", prepared.RequestBody["max_tokens"])
			}
		})
	}
}

func TestAnthropicRejectsOutputBudgetAboveModelLimit(t *testing.T) {
	for _, stage := range []bool{true, false} {
		params := protocolStreamParams{
			provider: models.ProviderDefinition{BaseURL: "https://example.com", APIKey: "test"},
			model:    models.ModelDefinition{Key: "haiku", Protocol: "ANTHROPIC", MaxOutputTokens: 128000},
			messages: []openAIMessage{{Role: "user", Content: "hi"}},
		}
		if stage {
			params.stageSettings.MaxOutputTokens = 128001
		} else {
			params.protocolConfig.Compat = map[string]any{"request": map[string]any{"always": map[string]any{"max_tokens": 128001}}}
		}
		if _, err := (&anthropicProtocol{}).PrepareRequest(params); err == nil || !strings.Contains(err.Error(), "maxOutputTokens (128000)") {
			t.Fatalf("expected budget above model limit to fail, got %v", err)
		}
	}
}

func TestAnthropicModelLimitDoesNotSetOpenAIRequestBudget(t *testing.T) {
	params := protocolStreamParams{
		provider: models.ProviderDefinition{BaseURL: "https://example.com", APIKey: "test"},
		model:    models.ModelDefinition{Protocol: "OPENAI", MaxOutputTokens: 128000},
		messages: []openAIMessage{{Role: "user", Content: "hi"}},
	}
	for _, p := range []providerProtocol{&openAIProtocol{}, &responsesProtocol{}} {
		prepared, err := p.PrepareRequest(params)
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
			if _, exists := prepared.RequestBody[key]; exists {
				t.Fatalf("model limit unexpectedly changed another protocol's request: %s", key)
			}
		}
	}
}
