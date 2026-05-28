package models

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadModelRegistryKeepsPlaintextProviderAPIKey(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, "apiKey: plain-text")

	registry, err := LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("LoadModelRegistry returned error: %v", err)
	}

	_, provider, err := registry.Get("mock-model")
	if err != nil {
		t.Fatalf("registry.Get returned error: %v", err)
	}
	if provider.APIKey != "plain-text" {
		t.Fatalf("expected plaintext apiKey, got %q", provider.APIKey)
	}
}

func TestLoadModelRegistryDecryptsProviderAPIKey(t *testing.T) {
	root := t.TempDir()
	t.Setenv(providerAPIKeyEnvPartKey, "env-secret")
	writeTestProviderAndModel(t, root, "apiKey: "+mustEncryptProviderAPIKeyForTest(t, "env-secret", "plain-text"))

	registry, err := LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("LoadModelRegistry returned error: %v", err)
	}

	_, provider, err := registry.Get("mock-model")
	if err != nil {
		t.Fatalf("registry.Get returned error: %v", err)
	}
	if provider.APIKey != "plain-text" {
		t.Fatalf("expected decrypted apiKey, got %q", provider.APIKey)
	}
}

func TestLoadModelRegistryReturnsDecryptErrorForInvalidAESProviderAPIKey(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, "apiKey: AES(v1:not-base64)")

	_, err := LoadModelRegistry(root)
	if err == nil {
		t.Fatal("expected LoadModelRegistry to fail")
	}
	if !strings.Contains(err.Error(), "resolve provider mock apiKey") || !strings.Contains(err.Error(), "invalid AES payload format") {
		t.Fatalf("expected decrypt error, got %v", err)
	}
}

func TestLoadModelRegistryParsesProviderMemoryEmbedding(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, strings.Join([]string{
		"apiKey: plain-text",
		"memory:",
		"  embedding:",
		"    model: text-embedding-3-small",
		"    dimension: 1536",
		"    timeoutMs: 15000",
	}, "\n"))

	registry, err := LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("LoadModelRegistry returned error: %v", err)
	}

	provider, err := registry.GetProvider("mock")
	if err != nil {
		t.Fatalf("GetProvider returned error: %v", err)
	}
	if provider.Memory.Embedding.Model != "text-embedding-3-small" {
		t.Fatalf("unexpected embedding model: %q", provider.Memory.Embedding.Model)
	}
	if provider.Memory.Embedding.Dimension != 1536 {
		t.Fatalf("unexpected embedding dimension: %d", provider.Memory.Embedding.Dimension)
	}
	if provider.Memory.Embedding.TimeoutMs != 15000 {
		t.Fatalf("unexpected embedding timeout: %d", provider.Memory.Embedding.TimeoutMs)
	}
}

func TestLoadModelRegistryDefaultsModelVisionToFalse(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, "apiKey: plain-text", "name: Mock Model")

	registry, err := LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("LoadModelRegistry returned error: %v", err)
	}

	model, _, err := registry.Get("mock-model")
	if err != nil {
		t.Fatalf("registry.Get returned error: %v", err)
	}
	if model.IsVision {
		t.Fatal("expected model IsVision to default to false")
	}
	if model.Name != "Mock Model" {
		t.Fatalf("expected model name to parse, got %q", model.Name)
	}
}

func TestLoadModelRegistryParsesModelVisionTrue(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, "apiKey: plain-text", "isVision: true")

	registry, err := LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("LoadModelRegistry returned error: %v", err)
	}

	model, _, err := registry.Get("mock-model")
	if err != nil {
		t.Fatalf("registry.Get returned error: %v", err)
	}
	if !model.IsVision {
		t.Fatal("expected model IsVision to parse true")
	}
}

func TestLoadModelRegistryParsesModelVisionFalse(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, "apiKey: plain-text", "isVision: false")

	registry, err := LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("LoadModelRegistry returned error: %v", err)
	}

	model, _, err := registry.Get("mock-model")
	if err != nil {
		t.Fatalf("registry.Get returned error: %v", err)
	}
	if model.IsVision {
		t.Fatal("expected model IsVision to parse false")
	}
}

func TestProviderlessModelCanBeListedAndReadWithoutProvider(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, "apiKey: plain-text")
	writeTestProviderlessModel(t, root, "gpt-5-codex", "gpt-5-codex")

	registry, err := LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("LoadModelRegistry returned error: %v", err)
	}

	model, err := registry.GetModel("gpt-5-codex")
	if err != nil {
		t.Fatalf("GetModel returned error: %v", err)
	}
	if model.Key != "gpt-5-codex" || model.ModelID != "gpt-5-codex" || model.Provider != "" {
		t.Fatalf("unexpected providerless model %#v", model)
	}

	found := false
	for _, item := range registry.List() {
		if item.Key == "gpt-5-codex" && item.ModelID == "gpt-5-codex" && item.Provider == "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected providerless model in List")
	}
}

func TestProviderlessModelStillFailsProviderBackedGet(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, "apiKey: plain-text")
	writeTestProviderlessModel(t, root, "gpt-5-codex", "gpt-5-codex")

	registry, err := LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("LoadModelRegistry returned error: %v", err)
	}

	if _, _, err := registry.Get("gpt-5-codex"); err == nil || !strings.Contains(err.Error(), "provider") {
		t.Fatalf("expected provider-backed Get to fail, got %v", err)
	}
}

func TestDefaultSkipsProviderlessModels(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, "apiKey: plain-text")
	writeTestProviderlessModel(t, root, "aaa-codex", "gpt-5-codex")

	registry, err := LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("LoadModelRegistry returned error: %v", err)
	}

	model, provider, err := registry.Default()
	if err != nil {
		t.Fatalf("Default returned error: %v", err)
	}
	if model.Key != "mock-model" || provider.Key != "mock" {
		t.Fatalf("expected provider-backed default, got model=%#v provider=%#v", model, provider)
	}
}

func writeTestProviderAndModel(t *testing.T, root string, apiKeyLine string, modelLines ...string) {
	t.Helper()

	providersDir := filepath.Join(root, "providers")
	modelsDir := filepath.Join(root, "models")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatalf("mkdir providers dir: %v", err)
	}
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		t.Fatalf("mkdir models dir: %v", err)
	}

	providerConfig := strings.Join([]string{
		"key: mock",
		"baseUrl: https://example.com",
		apiKeyLine,
		"defaultModel: mock-model",
	}, "\n")
	if err := os.WriteFile(filepath.Join(providersDir, "mock.yml"), []byte(providerConfig), 0o644); err != nil {
		t.Fatalf("write provider config: %v", err)
	}

	modelConfig := strings.Join([]string{
		"key: mock-model",
		"provider: mock",
		"protocol: OPENAI",
		"modelId: mock-model-id",
	}, "\n")
	if len(modelLines) > 0 {
		modelConfig += "\n" + strings.Join(modelLines, "\n")
	}
	if err := os.WriteFile(filepath.Join(modelsDir, "mock-model.yml"), []byte(modelConfig), 0o644); err != nil {
		t.Fatalf("write model config: %v", err)
	}
}

func writeTestProviderlessModel(t *testing.T, root string, key string, modelID string) {
	t.Helper()
	modelsDir := filepath.Join(root, "models")
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		t.Fatalf("mkdir models dir: %v", err)
	}
	modelConfig := strings.Join([]string{
		"key: " + key,
		"name: Providerless Model",
		"modelId: " + modelID,
	}, "\n")
	if err := os.WriteFile(filepath.Join(modelsDir, key+".yml"), []byte(modelConfig), 0o644); err != nil {
		t.Fatalf("write providerless model config: %v", err)
	}
}
