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

func writeTestProviderAndModel(t *testing.T, root string, apiKeyLine string) {
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
	if err := os.WriteFile(filepath.Join(modelsDir, "mock-model.yml"), []byte(modelConfig), 0o644); err != nil {
		t.Fatalf("write model config: %v", err)
	}
}
