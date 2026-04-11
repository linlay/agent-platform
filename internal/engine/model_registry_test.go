package engine

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/config"
)

func TestLoadProvidersReadsOpenAIEndpointFromProtocolConfig(t *testing.T) {
	dir := t.TempDir()
	writeProviderFixture(t, dir, "compat_provider.yml", ""+
		"key: compat_provider\n"+
		"baseUrl: https://provider.example.com\n"+
		"apiKey: test-key\n"+
		"defaultModel: reasoner-model\n"+
		"protocols:\n"+
		"  OPENAI:\n"+
		"    endpointPath: v1/chat/completions\n")

	providers, err := loadProviders(dir)
	if err != nil {
		t.Fatalf("load providers: %v", err)
	}

	provider := providers["compat_provider"]
	if provider.EndpointPath != "/v1/chat/completions" {
		t.Fatalf("expected normalized endpoint path, got %q", provider.EndpointPath)
	}
	if provider.Protocol("OPENAI").EndpointPath != "/v1/chat/completions" {
		t.Fatalf("expected openai protocol endpoint path, got %#v", provider.Protocols)
	}
}

func TestLoadProvidersDefaultsEndpointPathFromBaseURL(t *testing.T) {
	dir := t.TempDir()
	writeProviderFixture(t, dir, "compat_provider.yml", ""+
		"key: compat_provider\n"+
		"baseUrl: https://provider.example.com/v1\n"+
		"apiKey: test-key\n"+
		"defaultModel: reasoner-model\n"+
		"protocols:\n"+
		"  OPENAI:\n"+
		"    compat:\n"+
		"      request:\n"+
		"        whenReasoningEnabled:\n"+
		"          reasoning_split: true\n")

	providers, err := loadProviders(dir)
	if err != nil {
		t.Fatalf("load providers: %v", err)
	}

	provider := providers["compat_provider"]
	if provider.EndpointPath != "/chat/completions" {
		t.Fatalf("expected /chat/completions for baseUrl ending in /v1, got %q", provider.EndpointPath)
	}
}

func TestLoadProvidersFallsBackToTopLevelEndpointPath(t *testing.T) {
	dir := t.TempDir()
	writeProviderFixture(t, dir, "legacy.yml", ""+
		"key: legacy\n"+
		"baseUrl: https://legacy.example.com\n"+
		"apiKey: test-key\n"+
		"defaultModel: legacy-model\n"+
		"endpointPath: chat/completions\n")

	providers, err := loadProviders(dir)
	if err != nil {
		t.Fatalf("load providers: %v", err)
	}

	provider := providers["legacy"]
	if provider.EndpointPath != "/chat/completions" {
		t.Fatalf("expected top-level endpointPath compatibility, got %q", provider.EndpointPath)
	}
}

func TestLoadProvidersSupportsBaseURLEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	writeProviderFixture(t, dir, "compat_provider.yml", ""+
		"key: compat_provider\n"+
		"baseUrl: https://provider.example.com\n"+
		"apiKey: test-key\n"+
		"defaultModel: reasoner-model\n"+
		"protocols:\n"+
		"  OPENAI:\n"+
		"    compat:\n"+
		"      request:\n"+
		"        whenReasoningEnabled:\n"+
		"          reasoning_split: true\n")

	t.Setenv("OPENAI_BASE_URL", "https://shared.example/v1")
	t.Setenv("COMPAT_PROVIDER_BASE_URL", "https://provider-override.example/v1")

	providers, err := loadProviders(dir)
	if err != nil {
		t.Fatalf("load providers: %v", err)
	}

	provider := providers["compat_provider"]
	if provider.BaseURL != "https://provider-override.example/v1" {
		t.Fatalf("expected provider-specific baseUrl override, got %q", provider.BaseURL)
	}
	if provider.EndpointPath != "/chat/completions" {
		t.Fatalf("expected endpoint to adjust to provider override, got %q", provider.EndpointPath)
	}

	t.Setenv("COMPAT_PROVIDER_BASE_URL", "")
	providers, err = loadProviders(dir)
	if err != nil {
		t.Fatalf("reload providers: %v", err)
	}
	provider = providers["compat_provider"]
	if provider.BaseURL != "https://shared.example/v1" {
		t.Fatalf("expected OPENAI_BASE_URL fallback, got %q", provider.BaseURL)
	}
}

func TestLoadProvidersSupportsAnthropicProtocolMetadata(t *testing.T) {
	dir := t.TempDir()
	writeProviderFixture(t, dir, "protocol_provider.yml", ""+
		"key: protocol_provider\n"+
		"baseUrl: https://provider.example.com\n"+
		"apiKey: test-key\n"+
		"defaultModel: protocol-model\n"+
		"protocols:\n"+
		"  ANTHROPIC:\n"+
		"    endpointPath: v1/messages\n"+
		"    headers:\n"+
		"      anthropic-version: 2023-06-01\n"+
		"    compat:\n"+
		"      request:\n"+
		"        whenReasoningEnabled:\n"+
		"          thinking:\n"+
		"            budget_tokens: 2048\n")

	providers, err := loadProviders(dir)
	if err != nil {
		t.Fatalf("load providers: %v", err)
	}

	protocol := providers["protocol_provider"].Protocol("ANTHROPIC")
	if protocol.EndpointPath != "/v1/messages" {
		t.Fatalf("expected anthropic endpoint path, got %#v", protocol)
	}
	if protocol.Headers["anthropic-version"] != "2023-06-01" {
		t.Fatalf("expected anthropic headers, got %#v", protocol.Headers)
	}
	requestCompat, _ := anyMapNode(protocol.Compat["request"])["whenReasoningEnabled"].(map[string]any)
	if requestCompat == nil {
		t.Fatalf("expected anthropic compat, got %#v", protocol.Compat)
	}
}

func TestLoadModelsSupportsHeadersAndCompat(t *testing.T) {
	dir := t.TempDir()
	writeProviderFixture(t, dir, "protocol_model.yml", ""+
		"key: protocol-model\n"+
		"provider: protocol_provider\n"+
		"protocol: ANTHROPIC\n"+
		"modelId: protocol-model\n"+
		"headers:\n"+
		"  anthropic-beta: tools-2024-04-04\n"+
		"compat:\n"+
		"  request:\n"+
		"    whenReasoningEnabled:\n"+
		"      thinking:\n"+
		"        budget_tokens: 4096\n")

	models, err := loadModels(dir)
	if err != nil {
		t.Fatalf("load models: %v", err)
	}

	model := models["protocol-model"]
	if model.Protocol != "ANTHROPIC" {
		t.Fatalf("expected anthropic protocol, got %#v", model)
	}
	if model.Headers["anthropic-beta"] != "tools-2024-04-04" {
		t.Fatalf("expected model headers, got %#v", model.Headers)
	}
	requestCompat, _ := anyMapNode(model.Compat["request"])["whenReasoningEnabled"].(map[string]any)
	if requestCompat == nil {
		t.Fatalf("expected model compat, got %#v", model.Compat)
	}
}

func TestLLMAgentEngineAvoidsDuplicateV1InProviderURL(t *testing.T) {
	registry := &ModelRegistry{
		providers: map[string]ProviderDefinition{
			"compat_provider": {
				Key:          "compat_provider",
				BaseURL:      "https://provider.example.com/v1",
				APIKey:       "test-key",
				DefaultModel: "reasoner-model",
				EndpointPath: defaultOpenAIEndpointPath("https://provider.example.com/v1"),
			},
		},
		models: map[string]ModelDefinition{
			"reasoner-model": {
				Key:      "reasoner-model",
				Provider: "compat_provider",
				Protocol: "OPENAI",
				ModelID:  "reasoner-model",
			},
		},
	}

	var requestURL string
	client := newScriptedHTTPClient(func(req *http.Request) scriptedHTTPResponse {
		requestURL = req.URL.String()
		return scriptedSSE(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, `[DONE]`)
	})

	engine := NewLLMAgentEngineWithHTTPClient(
		config.Config{Defaults: config.DefaultsConfig{React: config.ReactDefaultsConfig{MaxSteps: 4}}},
		registry,
		&testToolExecutor{},
		NewNoopSandboxClient(),
		client,
	)

	stream, err := engine.Stream(context.Background(), api.QueryRequest{Message: "hi"}, QuerySession{
		RunID:    "run_provider_url",
		ChatID:   "chat_provider_url",
		ModelKey: "reasoner-model",
	})
	if err != nil {
		t.Fatalf("stream query: %v", err)
	}
	defer stream.Close()
	drainAgentStream(t, stream)

	if requestURL != "https://provider.example.com/v1/chat/completions" {
		t.Fatalf("expected canonical provider url, got %q", requestURL)
	}
}

func TestModelRegistryDefaultPrefersExplicitDefaultModelKeyAcrossProtocols(t *testing.T) {
	registry := &ModelRegistry{
		providers: map[string]ProviderDefinition{
			"compat_provider": {
				Key:          "compat_provider",
				BaseURL:      "https://provider.example.com",
				APIKey:       "test-key",
				DefaultModel: "reasoner-model-openai",
			},
		},
		models: map[string]ModelDefinition{
			"reasoner-model-openai": {
				Key:      "reasoner-model-openai",
				Provider: "compat_provider",
				Protocol: "OPENAI",
				ModelID:  "shared-model-id",
			},
			"reasoner-model-anthropic": {
				Key:      "reasoner-model-anthropic",
				Provider: "compat_provider",
				Protocol: "ANTHROPIC",
				ModelID:  "shared-model-id",
			},
		},
	}

	model, _, err := registry.Default()
	if err != nil {
		t.Fatalf("default: %v", err)
	}
	if model.Key != "reasoner-model-openai" {
		t.Fatalf("expected explicit default model key, got %#v", model)
	}
}

func writeProviderFixture(t *testing.T, dir string, name string, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644); err != nil {
		t.Fatalf("write provider fixture: %v", err)
	}
}
