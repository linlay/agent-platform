package models

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"strings"
	"testing"
)

func TestResolveProviderAPIKeyReturnsPlaintextUnchanged(t *testing.T) {
	got, err := resolveProviderAPIKey("mock", "test-key")
	if err != nil {
		t.Fatalf("resolveProviderAPIKey returned error: %v", err)
	}
	if got != "test-key" {
		t.Fatalf("expected plaintext apiKey to stay unchanged, got %q", got)
	}
}

func TestResolveProviderAPIKeyDecryptsAESPayload(t *testing.T) {
	t.Setenv(providerAPIKeyEnvPartKey, "env-secret")

	ciphertext := mustEncryptProviderAPIKeyForTest(t, "env-secret", "test-key")
	got, err := resolveProviderAPIKey("mock", ciphertext)
	if err != nil {
		t.Fatalf("resolveProviderAPIKey returned error: %v", err)
	}
	if got != "test-key" {
		t.Fatalf("expected decrypted apiKey, got %q", got)
	}
}

func TestResolveProviderAPIKeyErrorsWhenEnvPartMissing(t *testing.T) {
	ciphertext := mustEncryptProviderAPIKeyForTest(t, "env-secret", "test-key")

	_, err := resolveProviderAPIKey("mock", ciphertext)
	if err == nil || !strings.Contains(err.Error(), "missing "+providerAPIKeyEnvPartKey) {
		t.Fatalf("expected missing env error, got %v", err)
	}
}

func TestResolveProviderAPIKeyErrorsWhenVersionMissing(t *testing.T) {
	t.Setenv(providerAPIKeyEnvPartKey, "env-secret")

	_, err := resolveProviderAPIKey("mock", "AES(not-versioned)")
	if err == nil || !strings.Contains(err.Error(), "invalid AES payload format") {
		t.Fatalf("expected invalid format error, got %v", err)
	}
}

func TestResolveProviderAPIKeyErrorsWhenBase64Invalid(t *testing.T) {
	t.Setenv(providerAPIKeyEnvPartKey, "env-secret")

	_, err := resolveProviderAPIKey("mock", "AES(v1:not-base64!!!)")
	if err == nil || !strings.Contains(err.Error(), "invalid base64 payload") {
		t.Fatalf("expected invalid base64 error, got %v", err)
	}
}

func TestResolveProviderAPIKeyErrorsWhenNonceTooShort(t *testing.T) {
	t.Setenv(providerAPIKeyEnvPartKey, "env-secret")

	payload := base64.RawURLEncoding.EncodeToString([]byte("short"))
	_, err := resolveProviderAPIKey("mock", "AES(v1:"+payload+")")
	if err == nil || !strings.Contains(err.Error(), "invalid nonce length") {
		t.Fatalf("expected invalid nonce error, got %v", err)
	}
}

func TestResolveProviderAPIKeyErrorsWhenKeyMismatch(t *testing.T) {
	t.Setenv(providerAPIKeyEnvPartKey, "wrong-secret")

	ciphertext := mustEncryptProviderAPIKeyForTest(t, "env-secret", "test-key")
	_, err := resolveProviderAPIKey("mock", ciphertext)
	if err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("expected authentication error, got %v", err)
	}
}

func TestResolveProviderAPIKeyErrorsWhenPlaintextEmpty(t *testing.T) {
	t.Setenv(providerAPIKeyEnvPartKey, "env-secret")

	ciphertext := mustEncryptProviderAPIKeyForTest(t, "env-secret", "   ")
	_, err := resolveProviderAPIKey("mock", ciphertext)
	if err == nil || !strings.Contains(err.Error(), "empty plaintext") {
		t.Fatalf("expected empty plaintext error, got %v", err)
	}
}

func mustEncryptProviderAPIKeyForTest(t *testing.T, envPart string, plaintext string) string {
	t.Helper()

	block, err := aes.NewCipher(deriveProviderAPIKeyMaterial(envPart))
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("new gcm: %v", err)
	}

	nonce := []byte("0123456789ab")
	data := gcm.Seal(nil, nonce, []byte(plaintext), nil)
	payload := append(append([]byte{}, nonce...), data...)
	return "AES(" + providerAPIKeyCipherVersion + ":" + base64.RawURLEncoding.EncodeToString(payload) + ")"
}
