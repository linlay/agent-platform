package models

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

type ProviderDefinition struct {
	Key          string
	BaseURL      string
	APIKey       string
	DefaultModel string
	EndpointPath string
	Protocols    map[string]ProtocolDefinition
	Embedding    ProviderEmbeddingConfig
	Memory       ProviderMemoryConfig
}

type ProtocolDefinition struct {
	EndpointPath string
	Headers      map[string]string
	Compat       map[string]any
}

type ProviderMemoryConfig struct {
	Embedding ProviderMemoryEmbeddingConfig
}

type ProviderMemoryEmbeddingConfig struct {
	Model     string
	Dimension int
	Timeout   int
}

type ProviderEmbeddingConfig struct {
	Model     string
	Dimension int
	Timeout   int
}

type ModelDefinition struct {
	Key              string
	Name             string
	Provider         string
	Type             string
	Protocol         string
	ModelID          string
	IsFunction       bool
	IsReasoner       bool
	IsVision         bool
	ContextWindow    int
	Pricing          ModelPricing
	Headers          map[string]string
	Compat           map[string]any
	ReasoningEfforts []string
	ServiceTiers     []string
	Embedding        ModelEmbeddingConfig
	Image            ModelImageConfig
}

const (
	ProtocolACPPassthrough = "ACP_PASSTHROUGH"

	ModelTypeChat            = "chat"
	ModelTypeEmbedding       = "embedding"
	ModelTypeImageGeneration = "image-generation"
)

type ModelEmbeddingConfig struct {
	Dimension    int
	Timeout      int
	EndpointPath string
}

type ModelImageConfig struct {
	EndpointPath    string
	Timeout         int
	DefaultSize     string
	ResponseFormats []string
}

type ModelPricing struct {
	Currency       string
	Unit           string
	InputCacheHit  float64
	InputCacheMiss float64
	Output         float64
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

func IsACPPassthroughProtocol(protocol string) bool {
	return strings.EqualFold(strings.TrimSpace(protocol), ProtocolACPPassthrough)
}

func IsACPPassthroughModel(model ModelDefinition) bool {
	return IsACPPassthroughProtocol(model.Protocol)
}

func NormalizeModelType(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return ModelTypeChat, true
	case ModelTypeChat:
		return ModelTypeChat, true
	case ModelTypeEmbedding:
		return ModelTypeEmbedding, true
	case ModelTypeImageGeneration:
		return ModelTypeImageGeneration, true
	default:
		return "", false
	}
}

func IsChatModel(model ModelDefinition) bool {
	modelType, ok := NormalizeModelType(model.Type)
	return ok && modelType == ModelTypeChat
}

func IsEmbeddingModel(model ModelDefinition) bool {
	modelType, ok := NormalizeModelType(model.Type)
	return ok && modelType == ModelTypeEmbedding
}

func IsImageGenerationModel(model ModelDefinition) bool {
	modelType, ok := NormalizeModelType(model.Type)
	return ok && modelType == ModelTypeImageGeneration
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
	return r.GetTyped(key, ModelTypeChat)
}

func (r *ModelRegistry) GetEmbedding(key string) (ModelDefinition, ProviderDefinition, error) {
	return r.GetTyped(key, ModelTypeEmbedding)
}

func (r *ModelRegistry) GetImageGeneration(key string) (ModelDefinition, ProviderDefinition, error) {
	return r.GetTyped(key, ModelTypeImageGeneration)
}

func (r *ModelRegistry) GetTyped(key string, modelType string) (ModelDefinition, ProviderDefinition, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	key = strings.TrimSpace(key)
	if key == "" {
		if modelType != ModelTypeChat {
			return ModelDefinition{}, ProviderDefinition{}, fmt.Errorf("%s model key is required", modelType)
		}
		return r.defaultLocked()
	}
	model, ok := r.models[key]
	if !ok {
		return ModelDefinition{}, ProviderDefinition{}, fmt.Errorf("model %s not found", key)
	}
	if normalized, ok := NormalizeModelType(model.Type); !ok || normalized != modelType {
		if !ok {
			return ModelDefinition{}, ProviderDefinition{}, fmt.Errorf("model %s has invalid type %q", model.Key, model.Type)
		}
		return ModelDefinition{}, ProviderDefinition{}, fmt.Errorf("model %s has type %s, want %s", model.Key, normalized, modelType)
	}
	if err := validateModelForRuntime(model, modelType); err != nil {
		return ModelDefinition{}, ProviderDefinition{}, err
	}
	if IsACPPassthroughModel(model) {
		return ModelDefinition{}, ProviderDefinition{}, fmt.Errorf("model %s uses ACP_PASSTHROUGH protocol and cannot be used by native provider runtime", model.Key)
	}
	provider, ok := r.providers[model.Provider]
	if !ok {
		return ModelDefinition{}, ProviderDefinition{}, fmt.Errorf("provider %s not found for model %s", model.Provider, model.Key)
	}
	return cloneModelDefinition(model), provider, nil
}

func validateModelForRuntime(model ModelDefinition, modelType string) error {
	if strings.TrimSpace(model.ModelID) == "" {
		return fmt.Errorf("model %s modelId is required", model.Key)
	}
	if modelType == ModelTypeEmbedding && model.Embedding.Dimension <= 0 {
		return fmt.Errorf("model %s embedding.dimension is required", model.Key)
	}
	return nil
}

func (r *ModelRegistry) GetModel(key string) (ModelDefinition, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	key = strings.TrimSpace(key)
	if key == "" {
		model, _, err := r.defaultLocked()
		return model, err
	}
	model, ok := r.models[key]
	if !ok {
		return ModelDefinition{}, fmt.Errorf("model %s not found", key)
	}
	return cloneModelDefinition(model), nil
}

func (r *ModelRegistry) List() []ModelDefinition {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	keys := make([]string, 0, len(r.models))
	for key := range r.models {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	items := make([]ModelDefinition, 0, len(keys))
	for _, key := range keys {
		items = append(items, cloneModelDefinition(r.models[key]))
	}
	return items
}

func cloneModelDefinition(model ModelDefinition) ModelDefinition {
	model.Headers = stringMapCopy(model.Headers)
	model.Compat = contracts.CloneAnyMap(model.Compat)
	model.ReasoningEfforts = append([]string(nil), model.ReasoningEfforts...)
	model.ServiceTiers = append([]string(nil), model.ServiceTiers...)
	model.Image.ResponseFormats = append([]string(nil), model.Image.ResponseFormats...)
	return model
}

func stringMapCopy(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func (r *ModelRegistry) GetProvider(key string) (ProviderDefinition, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	key = strings.TrimSpace(key)
	if key == "" {
		return ProviderDefinition{}, fmt.Errorf("empty provider key")
	}
	provider, ok := r.providers[key]
	if !ok {
		return ProviderDefinition{}, fmt.Errorf("provider %s not found", key)
	}
	return provider, nil
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
		if !providerHasAPIKey(provider) {
			continue
		}
		if match, ok := matchProviderDefault(r.models, provider); ok {
			return cloneModelDefinition(match), provider, nil
		}
	}
	modelKeys := make([]string, 0, len(r.models))
	for key := range r.models {
		modelKeys = append(modelKeys, key)
	}
	sort.Strings(modelKeys)
	for _, modelKey := range modelKeys {
		model := r.models[modelKey]
		if IsACPPassthroughModel(model) || !IsChatModel(model) {
			continue
		}
		provider, ok := r.providers[model.Provider]
		if ok && providerHasAPIKey(provider) {
			return cloneModelDefinition(model), provider, nil
		}
	}
	return ModelDefinition{}, ProviderDefinition{}, fmt.Errorf("no provider-backed models loaded from registries")
}

func matchProviderDefault(models map[string]ModelDefinition, provider ProviderDefinition) (ModelDefinition, bool) {
	if !providerHasAPIKey(provider) {
		return ModelDefinition{}, false
	}
	for _, model := range models {
		if IsACPPassthroughModel(model) || !IsChatModel(model) {
			continue
		}
		if model.Provider != provider.Key {
			continue
		}
		if strings.EqualFold(model.Key, provider.DefaultModel) || strings.EqualFold(model.ModelID, provider.DefaultModel) {
			return model, true
		}
	}
	return ModelDefinition{}, false
}

func providerHasAPIKey(provider ProviderDefinition) bool {
	return strings.TrimSpace(provider.APIKey) != ""
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
		apiKey := strings.TrimSpace(stringNode(values["apiKey"]))
		protocols := loadProviderProtocols(values, baseURL)
		memoryConfig, err := loadProviderMemory(values)
		if err != nil {
			return nil, fmt.Errorf("load provider %s: %w", entry.Name(), err)
		}
		embeddingConfig := loadProviderEmbedding(values)
		result[key] = ProviderDefinition{
			Key:          key,
			BaseURL:      baseURL,
			APIKey:       apiKey,
			DefaultModel: strings.TrimSpace(stringNode(values["defaultModel"])),
			EndpointPath: resolveProviderEndpointPath(values, baseURL, "OPENAI"),
			Protocols:    protocols,
			Embedding:    embeddingConfig,
			Memory:       memoryConfig,
		}
	}
	return result, nil
}

func loadProviderEmbedding(values map[string]any) ProviderEmbeddingConfig {
	embedding := nestedMap(values, "embedding")
	if embedding == nil {
		return ProviderEmbeddingConfig{}
	}
	return ProviderEmbeddingConfig{
		Model:     strings.TrimSpace(stringNode(embedding["model"])),
		Dimension: intNode(embedding["dimension"]),
		Timeout:   intNode(embedding["timeout"]),
	}
}

func loadProviderMemory(values map[string]any) (ProviderMemoryConfig, error) {
	embedding := nestedMap(values, "memory", "embedding")
	if embedding == nil {
		return ProviderMemoryConfig{}, nil
	}
	return ProviderMemoryConfig{
		Embedding: ProviderMemoryEmbeddingConfig{
			Model:     strings.TrimSpace(stringNode(embedding["model"])),
			Dimension: intNode(embedding["dimension"]),
			Timeout:   intNode(embedding["timeout"]),
		},
	}, nil
}

func resolveProviderBaseURL(key string, values map[string]any) string {
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
			Compat:       contracts.CloneAnyMap(contracts.AnyMapNode(node["compat"])),
		}
	}
	if _, ok := result["OPENAI"]; !ok {
		result["OPENAI"] = ProtocolDefinition{
			EndpointPath: resolveProviderEndpointPath(values, baseURL, "OPENAI"),
		}
	}
	return result
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
		modelType, ok := NormalizeModelType(stringNode(values["type"]))
		if !ok {
			return nil, fmt.Errorf("load model %s: invalid type %q", entry.Name(), stringNode(values["type"]))
		}
		result[key] = ModelDefinition{
			Key:              key,
			Name:             strings.TrimSpace(stringNode(values["name"])),
			Provider:         strings.TrimSpace(stringNode(values["provider"])),
			Type:             modelType,
			Protocol:         strings.ToUpper(strings.TrimSpace(stringNode(values["protocol"]))),
			ModelID:          strings.TrimSpace(stringNode(values["modelId"])),
			IsFunction:       parseTruthy(stringNode(values["isFunction"])),
			IsReasoner:       parseTruthy(stringNode(values["isReasoner"])),
			IsVision:         parseTruthyDefault(values["isVision"], false),
			ContextWindow:    contracts.AnyIntNode(values["maxInputTokens"]),
			Pricing:          loadModelPricing(values["pricing"]),
			Headers:          stringMapNode(values["headers"]),
			Compat:           contracts.CloneAnyMap(contracts.AnyMapNode(values["compat"])),
			ReasoningEfforts: stringSliceNode(values["reasoningEfforts"]),
			ServiceTiers:     stringSliceNode(values["serviceTiers"]),
			Embedding:        loadModelEmbedding(values["embedding"]),
			Image:            loadModelImage(values["image"]),
		}
	}
	return result, nil
}

func loadModelEmbedding(raw any) ModelEmbeddingConfig {
	values := contracts.AnyMapNode(raw)
	if len(values) == 0 {
		return ModelEmbeddingConfig{}
	}
	return ModelEmbeddingConfig{
		Dimension:    intNode(values["dimension"]),
		Timeout:      intNode(values["timeout"]),
		EndpointPath: strings.TrimSpace(contracts.FirstNonEmptyString(values["endpointPath"], values["endpoint-path"])),
	}
}

func loadModelImage(raw any) ModelImageConfig {
	values := contracts.AnyMapNode(raw)
	if len(values) == 0 {
		return ModelImageConfig{}
	}
	return ModelImageConfig{
		EndpointPath:    strings.TrimSpace(contracts.FirstNonEmptyString(values["endpointPath"], values["endpoint-path"])),
		Timeout:         intNode(values["timeout"]),
		DefaultSize:     strings.TrimSpace(contracts.FirstNonEmptyString(values["defaultSize"], values["default-size"])),
		ResponseFormats: firstStringSliceNode(values["responseFormats"], values["response-formats"]),
	}
}

func firstStringSliceNode(values ...any) []string {
	for _, value := range values {
		items := stringSliceNode(value)
		if len(items) > 0 {
			return items
		}
	}
	return nil
}

func loadModelPricing(raw any) ModelPricing {
	values := contracts.AnyMapNode(raw)
	if len(values) == 0 {
		return ModelPricing{}
	}
	return ModelPricing{
		Currency:       strings.ToUpper(strings.TrimSpace(stringNode(values["currency"]))),
		Unit:           strings.TrimSpace(stringNode(values["unit"])),
		InputCacheHit:  floatNode(values["inputCacheHit"]),
		InputCacheMiss: floatNode(values["inputCacheMiss"]),
		Output:         floatNode(values["output"]),
	}
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

func stringSliceNode(value any) []string {
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return nil
	}
	result := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		text := strings.TrimSpace(stringNode(item))
		if text == "" || seen[text] {
			continue
		}
		seen[text] = true
		result = append(result, text)
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
	case bool:
		if v {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

func intNode(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		return n
	default:
		return 0
	}
}

func floatNode(value any) float64 {
	switch v := value.(type) {
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case float64:
		return v
	case string:
		n, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return n
	default:
		return 0
	}
}

func parseTruthy(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func parseTruthyDefault(value any, defaultValue bool) bool {
	if value == nil {
		return defaultValue
	}
	return parseTruthy(stringNode(value))
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
