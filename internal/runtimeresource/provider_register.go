package runtimeresource

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"agent-platform/internal/config"
)

const (
	providerRegisterTimeout      = 20 * time.Second
	providerRegisterMaxBodyBytes = int64(1 << 20)
)

var (
	providerKeyPattern       = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	yamlPlainScalarPattern   = regexp.MustCompile(`^[A-Za-z0-9._~+/=-]+$`)
	defaultRegisterProviders = []string{"th-deepseek", "th-minimax"}
)

type providerRegisterConfig struct {
	Enabled   bool                  `json:"enabled"`
	Endpoint  string                `json:"endpoint"`
	Grant     providerRegisterGrant `json:"grant"`
	Providers []string              `json:"providers"`
}

type providerRegisterGrant struct {
	Type  string `json:"type"`
	Token string `json:"token"`
}

type providerRegisterTarget struct {
	providerKey  string
	providerPath string
	content      []byte
}

func applyProviderRegistration(candidateRoot, registerPath, desktopDeviceID string) (int, error) {
	if strings.TrimSpace(registerPath) == "" {
		return 0, nil
	}
	data, err := os.ReadFile(registerPath)
	if err != nil {
		return 0, fmt.Errorf("read %s from runtime resource package: %w", providerRegisterFile, err)
	}
	var register providerRegisterConfig
	if err := json.Unmarshal(data, &register); err != nil {
		return 0, fmt.Errorf("parse %s from runtime resource package: %w", providerRegisterFile, err)
	}
	if !register.Enabled {
		return 0, nil
	}
	endpoint, err := normalizeProviderRegisterEndpoint(register.Endpoint)
	if err != nil {
		return 0, err
	}
	token, err := normalizeProviderRegisterGrant(register.Grant)
	if err != nil {
		return 0, err
	}
	providers, err := normalizeProviderRegisterProviders(register.Providers)
	if err != nil {
		return 0, err
	}
	deviceID := strings.TrimSpace(desktopDeviceID)
	if deviceID == "" {
		return 0, fmt.Errorf("%s requires --desktop-device-id", providerRegisterFile)
	}
	targets := make([]providerRegisterTarget, 0, len(providers))
	for _, providerKey := range providers {
		providerPath := filepath.Join(candidateRoot, "registries", "providers", providerKey+".yml")
		content, err := os.ReadFile(providerPath)
		if err != nil {
			return 0, fmt.Errorf("provider registration target %s is missing: %w", providerKey, err)
		}
		parsed, err := config.LoadYAMLTreeBytes(content)
		if err != nil {
			return 0, fmt.Errorf("parse provider registration target %s: %w", providerKey, err)
		}
		if _, ok := parsed.(map[string]any); !ok {
			return 0, fmt.Errorf("provider registration target %s must be a YAML object", providerKey)
		}
		targets = append(targets, providerRegisterTarget{
			providerKey:  providerKey,
			providerPath: providerPath,
			content:      content,
		})
	}
	apiKey, err := requestProviderAPIKey(endpoint, token, deviceID)
	if err != nil {
		return 0, err
	}
	for _, target := range targets {
		updated := upsertProviderAPIKeyContent(string(target.content), apiKey)
		if err := os.WriteFile(target.providerPath, []byte(updated), 0o600); err != nil {
			return 0, fmt.Errorf("write regenerated API key for provider %s: %w", target.providerKey, err)
		}
	}
	return len(providers), nil
}

func normalizeProviderRegisterEndpoint(value string) (string, error) {
	endpoint := strings.TrimSpace(value)
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", fmt.Errorf("%s endpoint must be an http(s) URL without user info", providerRegisterFile)
	}
	return endpoint, nil
}

func normalizeProviderRegisterGrant(grant providerRegisterGrant) (string, error) {
	grantType := strings.ToLower(strings.TrimSpace(grant.Type))
	if grantType == "" {
		grantType = "jwt"
	}
	if grantType != "jwt" {
		return "", fmt.Errorf("%s grant.type only supports jwt", providerRegisterFile)
	}
	token := strings.TrimSpace(grant.Token)
	if token == "" {
		return "", fmt.Errorf("%s grant.token is required", providerRegisterFile)
	}
	return token, nil
}

func normalizeProviderRegisterProviders(values []string) ([]string, error) {
	if values == nil {
		values = defaultRegisterProviders
	}
	providers := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		provider := strings.TrimSpace(value)
		if !providerKeyPattern.MatchString(provider) {
			return nil, fmt.Errorf("%s contains an invalid provider key", providerRegisterFile)
		}
		if _, exists := seen[provider]; exists {
			continue
		}
		seen[provider] = struct{}{}
		providers = append(providers, provider)
	}
	if len(providers) == 0 {
		return nil, fmt.Errorf("%s providers is required", providerRegisterFile)
	}
	return providers, nil
}

func requestProviderAPIKey(endpoint, token, desktopDeviceID string) (string, error) {
	payload, err := json.Marshal(map[string]string{"name": desktopDeviceID})
	if err != nil {
		return "", fmt.Errorf("encode provider registration request: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), providerRegisterTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("create provider registration request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return fmt.Errorf("provider registration endpoint redirects are not allowed")
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("provider registration request failed: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, providerRegisterMaxBodyBytes+1))
	if err != nil {
		return "", fmt.Errorf("read provider registration response: %w", err)
	}
	if int64(len(body)) > providerRegisterMaxBodyBytes {
		return "", fmt.Errorf("provider registration response is too large")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("provider registration request failed: HTTP %d", response.StatusCode)
	}
	var result struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("provider registration response is not valid JSON")
	}
	apiKey := strings.TrimSpace(result.Key)
	if apiKey == "" {
		return "", fmt.Errorf("provider registration response is missing key")
	}
	return apiKey, nil
}

func upsertProviderAPIKeyContent(content, apiKey string) string {
	newline := "\n"
	if strings.Contains(content, "\r\n") {
		newline = "\r\n"
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	apiKeyValue := apiKey
	if !yamlPlainScalarPattern.MatchString(apiKey) {
		encoded, _ := json.Marshal(apiKey)
		apiKeyValue = string(encoded)
	}
	apiKeyLine := "apiKey: " + apiKeyValue
	for index, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if !strings.HasPrefix(trimmed, "apiKey:") {
			continue
		}
		indent := line[:len(line)-len(trimmed)]
		lines[index] = indent + apiKeyLine
		return strings.Join(lines, newline) + newline
	}
	insertAt := len(lines)
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "baseUrl:") {
			insertAt = index + 1
			break
		}
		if strings.HasPrefix(trimmed, "key:") && insertAt == len(lines) {
			insertAt = index + 1
		}
	}
	lines = append(lines, "")
	copy(lines[insertAt+1:], lines[insertAt:])
	lines[insertAt] = apiKeyLine
	return strings.Join(lines, newline) + newline
}
