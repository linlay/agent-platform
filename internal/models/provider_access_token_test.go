package models

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/apperrors"
)

func TestUseAccessTokenProviderReadsIdentityFilePerRequest(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, "useAccessToken: true\napiKey: dk_stale")
	identity := filepath.Join(t.TempDir(), "access-token")
	registry, err := LoadModelRegistryWithIdentityFile(root, identity)
	if err != nil {
		t.Fatal(err)
	}
	_, provider, err := registry.Get("mock-model")
	if err != nil {
		t.Fatal(err)
	}
	if !provider.UseAccessToken || provider.APIKey != "" || !provider.HasCredential() {
		t.Fatalf("access token provider: %+v", provider)
	}
	var coded *apperrors.Error
	if _, err = provider.Credential(); !errors.As(err, &coded) || coded.Code() != apperrors.CodeProviderNotConfigured {
		t.Fatalf("signed-out credential error = %v", err)
	}
	for _, token := range []string{"jwt-a", "jwt-b"} {
		if err = os.WriteFile(identity, []byte(token+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		name, value, err := provider.AuthHeader("X-Api-Key")
		if err != nil || name != "Authorization" || value != "Bearer "+token {
			t.Fatalf("auth header = %q %q %v", name, value, err)
		}
	}
}

func TestStaticProviderKeepsProtocolAPIKeyHeader(t *testing.T) {
	provider := ProviderDefinition{Key: "mock", APIKey: "dk_key"}
	if name, value, err := provider.AuthHeader("X-Api-Key"); err != nil || name != "X-Api-Key" || value != "dk_key" {
		t.Fatalf("anthropic header = %q %q %v", name, value, err)
	}
	if name, value, err := provider.AuthHeader(""); err != nil || name != "Authorization" || value != "Bearer dk_key" {
		t.Fatalf("bearer header = %q %q %v", name, value, err)
	}
	if (ProviderDefinition{Key: "empty"}).HasCredential() {
		t.Fatal("empty provider reported a credential")
	}
}
