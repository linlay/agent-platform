package models

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/contracts"
)

type ProviderDefinition struct {
	Key          string
	BaseURL      string
	APIKey       string
	DefaultModel string
	EndpointPath string
	Protocols    map[string]ProtocolDefinition
}

type ProtocolDefinition struct {
	EndpointPath string
	Headers      map[string]string
	Compat       map[string]any
}

type ModelDefinition struct {
	Key        string
	Provider   string
	Protocol   string
	ModelID    string
	IsFunction bool
	IsReasoner bool
	Headers    map[string]string
	Compat     map[string]any
}

type ModelRegistry struct {
	root string

	mu        sync.RWMutex
	providers map[string]ProviderDefinition
	models    map[string]ModelDefinition
}

func (p ProviderDefinition) Protocol(protocol string) ProtocolDefinition {
	normalized := strings.ToUpper(strings.TrimSpace(protocol))
	if normalized == "" {
		normalized = "OPENAI"
	}
	if def, ok := p.Protocols[normalized]; ok {
		return def
	}
	if normalized == "OPENAI" {
		return ProtocolDefinition{EndpointPath: p.EndpointPath}
	}
	return ProtocolDefinition{}
}

func LoadModelRegistry(registriesDir string) (*ModelRegistry, error) {
	registry := &ModelRegistry{root: registriesDir}
	if err := registry.Reload(); err != nil {
		return nil, err
	}
	return registry, nil
}

func (r *ModelRegistry) Reload() error {
	if err := r.ReloadProviders(); err != nil {
		return err
	}
	return r.ReloadModels()
}

// ReloadProviders reloads only provider definitions. Independent of models —
// model definitions still resolve providers by key from the latest map.
func (r *ModelRegistry) ReloadProviders() error {
	providers, err := loadProviders(filepath.Join(r.root, "providers"))
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.providers = providers
	r.mu.Unlock()
	return nil
}

// ReloadModels reloads only model definitions.
func (r *ModelRegistry) ReloadModels() error {
	models, err := loadModels(filepath.Join(r.root, "models"))
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.models = models
	r.mu.Unlock()
	return nil
}

func (r *ModelRegistry) Get(key string) (ModelDefinition, ProviderDefinition, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if strings.TrimSpace(key) == "" {
		return r.defaultLocked()
	}
	model, ok := r.models[key]
	if !ok {
		return ModelDefinition{}, ProviderDefinition{}, fmt.Errorf("model %s not found", key)
	}
	provider, ok := r.providers[model.Provider]
	if !ok {
		return ModelDefinition{}, ProviderDefinition{}, fmt.Errorf("provider %s not found for model %s", model.Provider, model.Key)
	}
	return model, provider, nil
}

func (r *ModelRegistry) Default() (ModelDefinition, ProviderDefinition, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.defaultLocked()
}

func (r *ModelRegistry) defaultLocked() (ModelDefinition, ProviderDefinition, error) {
	if len(r.models) == 0 {
		return ModelDefinition{}, ProviderDefinition{}, fmt.Errorf("no models loaded from registries")
	}
	providerKeys := make([]string, 0, len(r.providers))
	for key := range r.providers {
		providerKeys = append(providerKeys, key)
	}
	sort.Strings(providerKeys)
	for _, providerKey := range providerKeys {
		provider := r.providers[providerKey]
		if match, ok := matchProviderDefault(r.models, provider); ok {
			return match, provider, nil
		}
	}
	modelKeys := make([]string, 0, len(r.models))
	for key := range r.models {
		modelKeys = append(modelKeys, key)
	}
	sort.Strings(modelKeys)
	model := r.models[modelKeys[0]]
	provider, ok := r.providers[model.Provider]
	if !ok {
		return ModelDefinition{}, ProviderDefinition{}, fmt.Errorf("provider %s not found for model %s", model.Provider, model.Key)
	}
	return model, provider, nil
}

func matchProviderDefault(models map[string]ModelDefinition, provider ProviderDefinition) (ModelDefinition, bool) {
	for _, model := range models {
		if model.Provider != provider.Key {
			continue
		}
		if strings.EqualFold(model.Key, provider.DefaultModel) || strings.EqualFold(model.ModelID, provider.DefaultModel) {
			return model, true
		}
	}
	return ModelDefinition{}, false
}

func loadProviders(dir string) (map[string]ProviderDefinition, error) {
	result := map[string]ProviderDefinition{}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read providers dir: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !shouldLoadRuntimeName(name) || (!strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml")) {
			continue
		}
		tree, err := config.LoadYAMLTree(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("load provider %s: %w", entry.Name(), err)
		}
		values, _ := tree.(map[string]any)
		key := strings.TrimSpace(stringNode(values["key"]))
		if key == "" {
			continue
		}
		baseURL := resolveProviderBaseURL(key, values)
		rawAPIKey := strings.TrimSpace(stringNode(values["apiKey"]))
		apiKey, err := resolveProviderAPIKey(key, rawAPIKey)
		if err != nil {
			return nil, fmt.Errorf("resolve provider %s apiKey: %w", key, err)
		}
		protocols := loadProviderProtocols(values, baseURL)
		result[key] = ProviderDefinition{
			Key:          key,
			BaseURL:      baseURL,
			APIKey:       apiKey,
			DefaultModel: strings.TrimSpace(stringNode(values["defaultModel"])),
			EndpointPath: resolveProviderEndpointPath(values, baseURL, "OPENAI"),
			Protocols:    protocols,
		}
	}
	return result, nil
}

func resolveProviderBaseURL(key string, values map[string]any) string {
	if value := strings.TrimSpace(os.Getenv(providerBaseURLEnvKey(key))); value != "" {
		return value
	}
	if hasProtocolConfig(values, "OPENAI") {
		if value := strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")); value != "" {
			return value
		}
	}
	return strings.TrimSpace(stringNode(values["baseUrl"]))
}

func resolveProviderEndpointPath(values map[string]any, baseURL string, protocol string) string {
	normalizedProtocol := strings.ToUpper(strings.TrimSpace(protocol))
	if normalizedProtocol != "" {
		if protocolNode := nestedMap(values, "protocols", normalizedProtocol); protocolNode != nil {
			if value := strings.TrimSpace(stringNode(protocolNode["endpointPath"])); value != "" {
				return normalizeEndpointPath(value, baseURL, normalizedProtocol)
			}
		}
	}
	if normalizedProtocol == "" || normalizedProtocol == "OPENAI" {
		if value := strings.TrimSpace(stringNode(values["endpointPath"])); value != "" {
			return normalizeEndpointPath(value, baseURL, normalizedProtocol)
		}
	}
	return defaultEndpointPath(normalizedProtocol, baseURL)
}

func hasProtocolConfig(values map[string]any, protocol string) bool {
	return nestedMap(values, "protocols", protocol) != nil
}

func nestedMap(values map[string]any, keys ...string) map[string]any {
	current := values
	for _, key := range keys {
		next, _ := current[key].(map[string]any)
		if next == nil {
			return nil
		}
		current = next
	}
	return current
}

func loadProviderProtocols(values map[string]any, baseURL string) map[string]ProtocolDefinition {
	result := map[string]ProtocolDefinition{}
	protocolNodes := contracts.AnyMapNode(values["protocols"])
	for rawProtocol, rawValue := range protocolNodes {
		protocol := strings.ToUpper(strings.TrimSpace(rawProtocol))
		if protocol == "" {
			continue
		}
		node := contracts.AnyMapNode(rawValue)
		result[protocol] = ProtocolDefinition{
			EndpointPath: resolveProviderEndpointPath(values, baseURL, protocol),
			Headers:      stringMapNode(node["headers"]),
			Compat:       cloneAnyMap(contracts.AnyMapNode(node["compat"])),
		}
	}
	if _, ok := result["OPENAI"]; !ok {
		result["OPENAI"] = ProtocolDefinition{
			EndpointPath: resolveProviderEndpointPath(values, baseURL, "OPENAI"),
		}
	}
	return result
}

func providerBaseURLEnvKey(key string) string {
	normalized := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r - ('a' - 'A')
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		default:
			return '_'
		}
	}, strings.TrimSpace(key))
	normalized = strings.Trim(normalized, "_")
	if normalized == "" {
		return "BASE_URL"
	}
	return normalized + "_BASE_URL"
}

func normalizeEndpointPath(value string, baseURL string, protocol string) string {
	path := "/" + strings.TrimLeft(strings.TrimSpace(value), "/")
	if basePath := normalizedBasePath(baseURL); basePath != "" && basePath != "/" && strings.HasPrefix(path, basePath+"/") {
		path = strings.TrimPrefix(path, basePath)
	}
	return path
}

func defaultEndpointPath(protocol string, baseURL string) string {
	switch strings.ToUpper(strings.TrimSpace(protocol)) {
	case "ANTHROPIC":
		if normalizedBasePath(baseURL) == "/v1" {
			return "/messages"
		}
		return "/v1/messages"
	case "", "OPENAI":
		if normalizedBasePath(baseURL) == "/v1" {
			return "/chat/completions"
		}
		return "/v1/chat/completions"
	default:
		return ""
	}
}

func defaultOpenAIEndpointPath(baseURL string) string {
	return defaultEndpointPath("OPENAI", baseURL)
}

func normalizedBasePath(rawBaseURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawBaseURL))
	if err != nil {
		return ""
	}
	path := strings.TrimSpace(parsed.EscapedPath())
	if path == "" {
		path = strings.TrimSpace(parsed.Path)
	}
	if path == "" || path == "/" {
		return ""
	}
	return "/" + strings.Trim(strings.TrimSpace(path), "/")
}

func loadModels(dir string) (map[string]ModelDefinition, error) {
	result := map[string]ModelDefinition{}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read models dir: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !shouldLoadRuntimeName(name) || (!strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml")) {
			continue
		}
		tree, err := config.LoadYAMLTree(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("load model %s: %w", entry.Name(), err)
		}
		values, _ := tree.(map[string]any)
		key := strings.TrimSpace(stringNode(values["key"]))
		if key == "" {
			continue
		}
		result[key] = ModelDefinition{
			Key:        key,
			Provider:   strings.TrimSpace(stringNode(values["provider"])),
			Protocol:   strings.ToUpper(strings.TrimSpace(stringNode(values["protocol"]))),
			ModelID:    strings.TrimSpace(stringNode(values["modelId"])),
			IsFunction: parseTruthy(stringNode(values["isFunction"])),
			IsReasoner: parseTruthy(stringNode(values["isReasoner"])),
			Headers:    stringMapNode(values["headers"]),
			Compat:     cloneAnyMap(contracts.AnyMapNode(values["compat"])),
		}
	}
	return result, nil
}

func stringMapNode(value any) map[string]string {
	node := contracts.AnyMapNode(value)
	if len(node) == 0 {
		return nil
	}
	result := make(map[string]string, len(node))
	for key, raw := range node {
		if text := strings.TrimSpace(contracts.AnyStringNode(raw)); text != "" {
			result[key] = text
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func stringNode(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case int64:
		return fmt.Sprintf("%d", v)
	case int:
		return fmt.Sprintf("%d", v)
	default:
		return ""
	}
}

func defaultString(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func parseTruthy(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func cloneAnyMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func shouldLoadRuntimeName(rawName string) bool {
	name := strings.TrimSpace(rawName)
	if name == "" {
		return false
	}
	return !hasRuntimeMarker(name, ".example")
}

func hasRuntimeMarker(rawName string, marker string) bool {
	name := strings.ToLower(strings.TrimSpace(rawName))
	if name == "" {
		return false
	}
	if strings.HasSuffix(name, marker) {
		return true
	}
	ext := filepath.Ext(name)
	if ext == "" {
		return false
	}
	return strings.HasSuffix(strings.TrimSuffix(name, ext), marker)
}
