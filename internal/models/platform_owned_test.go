package models

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRegistryFiles(t *testing.T, provider string, model string) string {
	t.Helper()
	root := t.TempDir()
	for dir, content := range map[string]string{"providers": provider, "models": model} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, "mock.yml"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const testProvider = "key: mock\nbaseUrl: https://example.com\napiKey: k\n"

func TestLoadModelRegistryParsesRequestIDHeader(t *testing.T) {
	root := writeRegistryFiles(t, testProvider+"requestIdHeader: X-Babelark-Request-Id\n", "key: m\nprovider: mock\nprotocol: ANTHROPIC\nmodelId: m\n")
	registry, err := LoadModelRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, provider, err := registry.Get("m"); err != nil || provider.RequestIDHeader != "X-Babelark-Request-Id" {
		t.Fatalf("provider=%#v err=%v", provider, err)
	}
	for _, bad := range []string{"requestIdHeader: \"X Bad\"", "requestIdHeader: [a]"} {
		root := writeRegistryFiles(t, testProvider+bad+"\n", "key: m\nprovider: mock\nmodelId: m\n")
		if _, err := LoadModelRegistry(root); err == nil || !strings.Contains(err.Error(), "requestIdHeader") {
			t.Fatalf("%s: err=%v", bad, err)
		}
	}
}

func TestLoadModelRegistryRejectsPlatformOwnedCompatFields(t *testing.T) {
	cases := []struct{ provider, model, want string }{
		{testProvider, "key: m\nprovider: mock\nprotocol: ANTHROPIC\nmodelId: m\ncompat:\n  request:\n    always:\n      metadata:\n        user_id: static\n", "compat.request.always.metadata"},
		{testProvider, "key: m\nprovider: mock\nprotocol: OPENAI_RESPONSES\nmodelId: m\ncompat:\n  request:\n    whenReasoningEnabled:\n      prompt_cache_key: static\n", "compat.request.whenReasoningEnabled.prompt_cache_key"},
		{testProvider + "protocols:\n  ANTHROPIC:\n    compat:\n      request:\n        always:\n          metadata: {}\n", "key: m\nprovider: mock\nmodelId: m\n", "protocols.ANTHROPIC"},
	}
	for _, tc := range cases {
		root := writeRegistryFiles(t, tc.provider, tc.model)
		if _, err := LoadModelRegistry(root); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("want %q, got %v", tc.want, err)
		}
	}
	// Fields are owned per protocol: Chat Completions may still configure them.
	root := writeRegistryFiles(t, testProvider, "key: m\nprovider: mock\nprotocol: OPENAI\nmodelId: m\ncompat:\n  request:\n    always:\n      prompt_cache_key: static\n      metadata: {}\n")
	if _, err := LoadModelRegistry(root); err != nil {
		t.Fatal(err)
	}
}
