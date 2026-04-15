package models

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
)

const (
	providerAPIKeyEnvPartKey   = "PROVIDER_APIKEY_KEY_PART"
	providerAPIKeyCipherPrefix = "AES("
	providerAPIKeyCipherSuffix = ")"
	providerAPIKeyCodePart     = "zenmind-provider"
)

func resolveProviderAPIKey(providerKey, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if !strings.HasPrefix(raw, providerAPIKeyCipherPrefix) || !strings.HasSuffix(raw, providerAPIKeyCipherSuffix) {
		return raw, nil
	}
	return decryptProviderAPIKey(providerKey, raw)
}

func decryptProviderAPIKey(providerKey, wrapped string) (string, error) {
	trimmed := strings.TrimSpace(wrapped)
	payload := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, providerAPIKeyCipherPrefix), providerAPIKeyCipherSuffix))
	if payload == "" || strings.Contains(payload, ":") {
		return "", fmt.Errorf("provider %s apiKey decrypt failed: invalid AES payload format", providerKey)
	}

	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", fmt.Errorf("provider %s apiKey decrypt failed: invalid base64 payload", providerKey)
	}

	envPart := strings.TrimSpace(os.Getenv(providerAPIKeyEnvPartKey))
	if envPart == "" {
		return "", fmt.Errorf("provider %s apiKey decrypt failed: missing %s", providerKey, providerAPIKeyEnvPartKey)
	}

	block, err := aes.NewCipher(deriveProviderAPIKeyMaterial(envPart))
	if err != nil {
		return "", fmt.Errorf("provider %s apiKey decrypt failed: init cipher: %w", providerKey, err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("provider %s apiKey decrypt failed: init gcm: %w", providerKey, err)
	}
	if len(data) < gcm.NonceSize() {
		return "", fmt.Errorf("provider %s apiKey decrypt failed: invalid nonce length", providerKey)
	}

	plaintext, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
	if err != nil {
		return "", fmt.Errorf("provider %s apiKey decrypt failed: authentication failed", providerKey)
	}
	apiKey := strings.TrimSpace(string(plaintext))
	if apiKey == "" {
		return "", fmt.Errorf("provider %s apiKey decrypt failed: empty plaintext", providerKey)
	}
	return apiKey, nil
}

func deriveProviderAPIKeyMaterial(envPart string) []byte {
	sum := sha256.Sum256([]byte(providerAPIKeyCodePart + ":" + strings.TrimSpace(envPart)))
	return sum[:]
}
