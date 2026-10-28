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

	"agent-platform/internal/agentconfig"
	"agent-platform/internal/apperrors"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

type ProviderDefinition struct {
	Key     string
	BaseURL string
	APIKey  string
	// UseSsoIdToken sends the Desktop identity token (--sso-id-token-file) as the
	// bearer credential instead of apiKey; the file is read per request.
	UseSsoIdToken  bool
	SsoIdTokenFile string
	DefaultModel   string
	EndpointPath   string
	// RequestIDHeader names a relay's own request ID response header; it is
	// read before the generic request ID headers for every protocol.
	RequestIDHeader string
	Protocols       map[string]ProtocolDefinition
	Embedding       ProviderEmbeddingConfig
}

type ProtocolDefinition struct {
	EndpointPath string
	Headers      map[string]string
	Compat       map[string]any
}

type ProviderEmbeddingConfig struct {
	Model     string
	Dimension int
	Timeout   int
}

type ModelDefinition struct {
	Key                    string
	Name                   string
	Icon                   string
	Provider               string
	Type                   string
	Protocol               string
	ModelID                string
	IsFunction             bool
	IsReasoner             bool
	IsVision               bool
	ContextWindow          int
	MaxOutputTokens        int
	L1KeepRecentRounds     int
	Timeout                int
	Pricing                ModelPricing
	Headers                map[string]string
	Compat                 map[string]any
	ReasoningEfforts       []string
	ReasoningEffortMapping map[string]string
	ServiceTiers           []string
	Reranker               ModelRerankerConfig
	Embedding              ModelEmbeddingConfig
	Image                  ModelImageConfig
}

const (
	ProtocolACPPassthrough = "ACP_PASSTHROUGH"

	ModelTypeChat      = "chat"
	ModelTypeEmbedding = "embedding"
	ModelTypeReranker  = "reranker"
	ModelTypeImage     = "image"
	ModelTypeVL        = "vl"
	ModelTypeTTS       = "tts"

	// Speech protocols carry their own request and response code in
	// internal/tools; type: tts models must declare one of them.
	ProtocolMiniMaxTTS   = "MINIMAX_TTS"
	ProtocolDashScopeTTS = "DASHSCOPE_TTS"

	PricingUnitPer1MTokens      = "per_1m_tokens"
	PricingUnitPer10KCharacters = "per_10k_characters"
)

// TTSProtocols lists the speech protocols implemented by speech_generate.
var TTSProtocols = []string{ProtocolMiniMaxTTS, ProtocolDashScopeTTS}

type ModelRerankerConfig struct{ EndpointPath string }

type ModelEmbeddingConfig struct {
	Dimension    int
	BatchSize    int
	Timeout      int
	EndpointPath string
}

type ModelImageConfig struct {
	Timeout         int // Retained for loading existing models; image_generate uses tool/profile deadlines.
	DefaultSize     string
	ResponseFormats []string
	Generation      ModelImageGenerationConfig
	Edit            ModelImageEditConfig
}

type ModelImageGenerationConfig struct {
	EndpointPath       string
	RequestFormat      string
	OmitResponseFormat bool
}

type ModelImageEditConfig struct {
	EndpointPath       string
	RequestFormat      string
	OmitResponseFormat bool
	MaskProtocol       string
	Configured         bool
}

const (
	ImageGenerationRequestFormatOpenAIImagesJSON      = "openai-images-json"
	ImageGenerationRequestFormatOpenAIChatCompletions = "openai-chat-completions"
	ImageEditRequestFormatOpenAIImagesMultipart       = "openai-images-multipart"
	ImageEditRequestFormatOpenAIChatCompletions       = "openai-chat-completions"
	ImageMaskProtocolNone                             = "none"
	ImageMaskProtocolOpenAIAlpha                      = "openai-alpha"
)

type ModelPricing struct {
	Currency       string
	Unit           string
	InputCacheHit  float64
	InputCacheMiss float64
	Output         float64
	// Characters is the price per 10k provider-billed characters; it applies
	// only with unit per_10k_characters.
	Characters float64
}

type ModelRegistry struct {
	root           string
	ssoIdTokenFile string

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

// ResolvedEndpointPath returns the endpoint configured under
// protocols.<protocol>, or the protocol's built-in default.
func (p ProviderDefinition) ResolvedEndpointPath(protocol string) string {
	if path := strings.TrimSpace(p.Protocol(protocol).EndpointPath); path != "" {
		return path
	}
	return defaultEndpointPath(protocol, p.BaseURL)
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
	case ModelTypeReranker:
		return ModelTypeReranker, true
	case ModelTypeImage:
		return ModelTypeImage, true
	case ModelTypeVL:
		return ModelTypeVL, true
	case ModelTypeTTS:
		return ModelTypeTTS, true
	default:
		return "", false
	}
}

// InvalidModelTypeMessage explains a rejected type, naming the replacement for
// renamed types.
func InvalidModelTypeMessage(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "image-generation") {
		return "model type image-generation was renamed to image"
	}
	return "model type must be chat, embedding, reranker, image, vl, or tts"
}

func IsTTSProtocol(protocol string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(protocol))
	for _, item := range TTSProtocols {
		if normalized == item {
			return true
		}
	}
	return false
}

// ValidateModelTypeProtocol keeps speech protocols and type: tts paired.
func ValidateModelTypeProtocol(modelType string, protocol string) error {
	if modelType == ModelTypeTTS {
		if !IsTTSProtocol(protocol) {
			return fmt.Errorf("type: tts requires protocol %s", strings.Join(TTSProtocols, " or "))
		}
		return nil
	}
	if IsTTSProtocol(protocol) {
		return fmt.Errorf("protocol %s is only supported for type: tts", strings.ToUpper(strings.TrimSpace(protocol)))
	}
	return nil
}

// ValidateModelPricing checks that character pricing is used only with its
// unit, and that tts models are not priced per token.
func ValidateModelPricing(pricing ModelPricing, modelType string) error {
	tokenPriced := pricing.InputCacheHit > 0 || pricing.InputCacheMiss > 0 || pricing.Output > 0
	if pricing.Unit == PricingUnitPer10KCharacters {
		if pricing.Characters <= 0 {
			return fmt.Errorf("pricing.characters must be greater than 0 for unit %s", PricingUnitPer10KCharacters)
		}
		if tokenPriced {
			return fmt.Errorf("pricing unit %s does not accept inputCacheHit, inputCacheMiss or output", PricingUnitPer10KCharacters)
		}
		return nil
	}
	if pricing.Characters > 0 {
		return fmt.Errorf("pricing.characters requires unit %s", PricingUnitPer10KCharacters)
	}
	if modelType == ModelTypeTTS && tokenPriced {
		return fmt.Errorf("type: tts pricing must use unit %s", PricingUnitPer10KCharacters)
	}
	return nil
}
func IsChatModel(model ModelDefinition) bool {
	modelType, ok := NormalizeModelType(model.Type)
	return ok && modelType == ModelTypeChat
}

func IsImageModel(model ModelDefinition) bool {
	modelType, ok := NormalizeModelType(model.Type)
	return ok && modelType == ModelTypeImage
}

func IsVLModel(model ModelDefinition) bool {
	modelType, ok := NormalizeModelType(model.Type)
	return ok && modelType == ModelTypeVL
}

func IsTTSModel(model ModelDefinition) bool {
	modelType, ok := NormalizeModelType(model.Type)
	return ok && modelType == ModelTypeTTS
}

func LoadModelRegistry(registriesDir string) (*ModelRegistry, error) {
	return LoadModelRegistryWithSsoIdTokenFile(registriesDir, "")
}

// LoadModelRegistryWithSsoIdTokenFile binds useSsoIdToken providers to the
// shared Desktop identity file.
func LoadModelRegistryWithSsoIdTokenFile(registriesDir, ssoIdTokenFile string) (*ModelRegistry, error) {
	registry := &ModelRegistry{root: registriesDir, ssoIdTokenFile: strings.TrimSpace(ssoIdTokenFile)}
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
	providers, err := loadProviders(filepath.Join(r.root, "providers"), r.ssoIdTokenFile)
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

func (r *ModelRegistry) GetImage(key string) (ModelDefinition, ProviderDefinition, error) {
	return r.GetTyped(key, ModelTypeImage)
}

func (r *ModelRegistry) GetVL(key string) (ModelDefinition, ProviderDefinition, error) {
	return r.GetTyped(key, ModelTypeVL)
}

func (r *ModelRegistry) GetTTS(key string) (ModelDefinition, ProviderDefinition, error) {
	return r.GetTyped(key, ModelTypeTTS)
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
	if modelType == ModelTypeReranker && strings.TrimSpace(model.Reranker.EndpointPath) == "" {
		return fmt.Errorf("model %s reranker.endpointPath is required", model.Key)
	}
	if modelType == ModelTypeEmbedding && model.Embedding.Dimension <= 0 {
		return fmt.Errorf("model %s embedding.dimension is required", model.Key)
	}
	if modelType == ModelTypeEmbedding && model.Embedding.BatchSize < 0 {
		return fmt.Errorf("model %s embedding.batchSize must be positive when configured", model.Key)
	}
	if modelType == ModelTypeImage {
		if err := ValidateModelImageConfig(model.Image); err != nil {
			return fmt.Errorf("model %s image: %w", model.Key, err)
		}
	}
	return nil
}

func ValidateModelImageConfig(image ModelImageConfig) error {
	if err := ValidateModelImageGenerationConfig(image.Generation); err != nil {
		return fmt.Errorf("generation: %w", err)
	}
	if err := ValidateModelImageEditConfig(image.Edit); err != nil {
		return fmt.Errorf("edit: %w", err)
	}
	return nil
}

func ValidateModelImageGenerationConfig(generation ModelImageGenerationConfig) error {
	if strings.TrimSpace(generation.EndpointPath) == "" {
		return fmt.Errorf("endpointPath is required")
	}
	switch strings.ToLower(strings.TrimSpace(generation.RequestFormat)) {
	case ImageGenerationRequestFormatOpenAIImagesJSON, ImageGenerationRequestFormatOpenAIChatCompletions:
		return nil
	default:
		return fmt.Errorf("requestFormat must be %s or %s", ImageGenerationRequestFormatOpenAIImagesJSON, ImageGenerationRequestFormatOpenAIChatCompletions)
	}
}

func ValidateModelImageEditConfig(edit ModelImageEditConfig) error {
	format := strings.ToLower(strings.TrimSpace(edit.RequestFormat))
	maskProtocol := strings.ToLower(strings.TrimSpace(edit.MaskProtocol))
	endpoint := strings.TrimSpace(edit.EndpointPath)
	if !edit.Configured && format == "" && maskProtocol == "" && endpoint == "" {
		return nil
	}
	if endpoint == "" {
		return fmt.Errorf("endpointPath is required")
	}
	switch format {
	case ImageEditRequestFormatOpenAIImagesMultipart, ImageEditRequestFormatOpenAIChatCompletions:
	default:
		return fmt.Errorf("requestFormat must be %s or %s", ImageEditRequestFormatOpenAIImagesMultipart, ImageEditRequestFormatOpenAIChatCompletions)
	}
	if maskProtocol == "" {
		maskProtocol = ImageMaskProtocolNone
	}
	switch maskProtocol {
	case ImageMaskProtocolNone:
	case ImageMaskProtocolOpenAIAlpha:
		if format != ImageEditRequestFormatOpenAIImagesMultipart {
			return fmt.Errorf("maskProtocol %s requires requestFormat %s", ImageMaskProtocolOpenAIAlpha, ImageEditRequestFormatOpenAIImagesMultipart)
		}
	default:
		return fmt.Errorf("maskProtocol must be %s or %s", ImageMaskProtocolNone, ImageMaskProtocolOpenAIAlpha)
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
	model.ReasoningEffortMapping = stringMapCopy(model.ReasoningEffortMapping)
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
	return provider.HasCredential()
}

// HasCredential reports whether a credential source is configured without
// reading secrets; an access token provider may still be signed out.
func (p ProviderDefinition) HasCredential() bool {
	return p.UseSsoIdToken || strings.TrimSpace(p.APIKey) != ""
}

// Credential resolves the secret for one outbound request. Access token
// providers re-read the identity file on every call so Desktop renewals reach
// running tasks without a provider reload.
func (p ProviderDefinition) Credential() (string, error) {
	if !p.UseSsoIdToken {
		// Static keys keep their existing callers' empty-key checks.
		return strings.TrimSpace(p.APIKey), nil
	}
	token, err := agentconfig.ReadSsoIdTokenFile(p.SsoIdTokenFile)
	if err != nil {
		return "", apperrors.Wrap(apperrors.CodeProviderNotConfigured, fmt.Errorf("provider %s access token unavailable: %w", p.Key, err))
	}
	if token == "" {
		return "", apperrors.New(apperrors.CodeProviderNotConfigured, fmt.Sprintf("provider %s requires a signed-in Desktop access token", p.Key))
	}
	return token, nil
}

// AuthHeader resolves the request credential header. Requests must call it
// per outbound request so an access token renewed by Desktop applies to the
// next call. Access token providers always use Authorization because relays
// accept identity tokens only there; apiKeyHeader (e.g. X-Api-Key) applies to
// static keys, empty means Bearer.
func (p ProviderDefinition) AuthHeader(apiKeyHeader string) (string, string, error) {
	credential, err := p.Credential()
	if err != nil {
		return "", "", err
	}
	if apiKeyHeader != "" && !p.UseSsoIdToken {
		return apiKeyHeader, credential, nil
	}
	return "Authorization", "Bearer " + credential, nil
}

func loadProviders(dir, ssoIdTokenFile string) (map[string]ProviderDefinition, error) {
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
		baseURL := resolveProviderBaseURL(values)
		apiKey := strings.TrimSpace(stringNode(values["apiKey"]))
		protocols, err := loadProviderProtocols(values, baseURL)
		if err != nil {
			return nil, fmt.Errorf("load provider %s: %w", entry.Name(), err)
		}
		if _, exists := values["memory"]; exists {
			return nil, fmt.Errorf("load provider %s: memory is no longer supported; remove this field (KBASE uses embedding models)", entry.Name())
		}
		requestIDHeader, err := ParseRequestIDHeader(values["requestIdHeader"])
		if err != nil {
			return nil, fmt.Errorf("load provider %s: %w", entry.Name(), err)
		}
		embeddingConfig := loadProviderEmbedding(values)
		if _, legacy := values["useAccessToken"]; legacy {
			return nil, fmt.Errorf("load provider %s: useAccessToken was replaced by useSsoIdToken; run deploy migration", entry.Name())
		}
		useSsoIdToken := parseTruthy(stringNode(values["useSsoIdToken"]))
		providerTokenFile := ""
		if useSsoIdToken {
			// apiKey is ignored so a stale registered key can never shadow the login identity.
			apiKey = ""
			providerTokenFile = ssoIdTokenFile
		}
		result[key] = ProviderDefinition{
			Key:             key,
			BaseURL:         baseURL,
			APIKey:          apiKey,
			UseSsoIdToken:   useSsoIdToken,
			SsoIdTokenFile:  providerTokenFile,
			DefaultModel:    strings.TrimSpace(stringNode(values["defaultModel"])),
			EndpointPath:    resolveProviderEndpointPath(values, baseURL, "OPENAI"),
			RequestIDHeader: requestIDHeader,
			Protocols:       protocols,
			Embedding:       embeddingConfig,
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

func resolveProviderBaseURL(values map[string]any) string {
	return strings.TrimSpace(stringNode(values["baseUrl"]))
}

func resolveProviderEndpointPath(values map[string]any, baseURL string, protocol string) string {
	normalizedProtocol := strings.ToUpper(strings.TrimSpace(protocol))
	if normalizedProtocol != "" {
		if protocolNode := nestedMap(values, "protocols", normalizedProtocol); protocolNode != nil {
			if value := strings.TrimSpace(stringNode(protocolNode["endpointPath"])); value != "" {
				return normalizeEndpointPath(value, baseURL)
			}
		}
	}
	if normalizedProtocol == "" || normalizedProtocol == "OPENAI" {
		if value := strings.TrimSpace(stringNode(values["endpointPath"])); value != "" {
			return normalizeEndpointPath(value, baseURL)
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

func loadProviderProtocols(values map[string]any, baseURL string) (map[string]ProtocolDefinition, error) {
	result := map[string]ProtocolDefinition{}
	protocolNodes := contracts.AnyMapNode(values["protocols"])
	for rawProtocol, rawValue := range protocolNodes {
		protocol := strings.ToUpper(strings.TrimSpace(rawProtocol))
		if protocol == "" {
			continue
		}
		node := contracts.AnyMapNode(rawValue)
		if protocol == "OPENAI" {
			if _, err := ParseOpenAIResponseCompat(node["compat"]); err != nil {
				return nil, fmt.Errorf("protocols.OPENAI: %w", err)
			}
		}
		if err := ValidatePlatformOwnedCompat(protocol, node["compat"]); err != nil {
			return nil, fmt.Errorf("protocols.%s: %w", protocol, err)
		}
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
	return result, nil
}

func normalizeEndpointPath(value string, baseURL string) string {
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
	case "OPENAI_RESPONSES":
		if normalizedBasePath(baseURL) == "/v1" {
			return "/responses"
		}
		return "/v1/responses"
	case "", "OPENAI":
		if normalizedBasePath(baseURL) == "/v1" {
			return "/chat/completions"
		}
		return "/v1/chat/completions"
	case ProtocolMiniMaxTTS:
		if normalizedBasePath(baseURL) == "/v1" {
			return "/t2a_v2"
		}
		return "/v1/t2a_v2"
	case ProtocolDashScopeTTS:
		if normalizedBasePath(baseURL) == "/api/v1" {
			return "/services/aigc/multimodal-generation/generation"
		}
		return "/api/v1/services/aigc/multimodal-generation/generation"
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
			return nil, fmt.Errorf("load model %s: invalid type %q: %s", entry.Name(), stringNode(values["type"]), InvalidModelTypeMessage(stringNode(values["type"])))
		}
		protocol := strings.ToUpper(strings.TrimSpace(stringNode(values["protocol"])))
		if IsACPPassthroughProtocol(protocol) && modelType != ModelTypeChat {
			return nil, fmt.Errorf("load model %s: ACP_PASSTHROUGH is only supported for type: chat", entry.Name())
		}
		if err := ValidateModelTypeProtocol(modelType, protocol); err != nil {
			return nil, fmt.Errorf("load model %s: %w", entry.Name(), err)
		}
		pricing := ParseModelPricing(values["pricing"])
		if err := ValidateModelPricing(pricing, modelType); err != nil {
			return nil, fmt.Errorf("load model %s: %w", entry.Name(), err)
		}
		reasoningEffortMapping, err := ParseReasoningEffortMapping(values["reasoningEffortMapping"], modelType, protocol)
		if err != nil {
			return nil, fmt.Errorf("load model %s: %w", entry.Name(), err)
		}
		if protocol == "" || protocol == "OPENAI" {
			if _, err := ParseOpenAIResponseCompat(values["compat"]); err != nil {
				return nil, fmt.Errorf("load model %s: %w", entry.Name(), err)
			}
		}
		if err := ValidatePlatformOwnedCompat(protocol, values["compat"]); err != nil {
			return nil, fmt.Errorf("load model %s: %w", entry.Name(), err)
		}
		keepRecent := contracts.AnyIntNode(values["l1KeepRecentRounds"])
		if raw, exists := values["l1KeepRecentRounds"]; exists && (keepRecent < 5 || keepRecent > 10 || fmt.Sprint(raw) != fmt.Sprint(keepRecent)) {
			return nil, fmt.Errorf("model %s: l1KeepRecentRounds must be an integer from 5 to 10", key)
		}
		maxOutput := contracts.AnyIntNode(values["maxOutputTokens"])
		if raw, exists := values["maxOutputTokens"]; exists && (maxOutput <= 0 || fmt.Sprint(raw) != fmt.Sprint(maxOutput)) {
			return nil, fmt.Errorf("model %s: maxOutputTokens must be a positive integer", key)
		}
		model := ModelDefinition{
			L1KeepRecentRounds:     keepRecent,
			Key:                    key,
			Name:                   strings.TrimSpace(stringNode(values["name"])),
			Icon:                   strings.TrimSpace(stringNode(values["icon"])),
			Provider:               strings.TrimSpace(stringNode(values["provider"])),
			Type:                   modelType,
			Protocol:               protocol,
			ModelID:                strings.TrimSpace(stringNode(values["modelId"])),
			IsFunction:             parseTruthy(stringNode(values["isFunction"])),
			IsReasoner:             parseTruthy(stringNode(values["isReasoner"])),
			IsVision:               parseTruthyDefault(values["isVision"], false),
			ContextWindow:          contracts.AnyIntNode(values["maxInputTokens"]),
			MaxOutputTokens:        maxOutput,
			Timeout:                intNode(values["timeout"]),
			Pricing:                pricing,
			Headers:                stringMapNode(values["headers"]),
			Compat:                 contracts.CloneAnyMap(contracts.AnyMapNode(values["compat"])),
			ReasoningEfforts:       stringSliceNode(values["reasoningEfforts"]),
			ReasoningEffortMapping: reasoningEffortMapping,
			ServiceTiers:           stringSliceNode(values["serviceTiers"]),
			Embedding:              loadModelEmbedding(values["embedding"]),
			Reranker:               ModelRerankerConfig{EndpointPath: strings.TrimSpace(stringNode(contracts.AnyMapNode(values["reranker"])["endpointPath"]))},
			Image:                  loadModelImage(values["image"]),
		}
		if modelType == ModelTypeImage {
			imageValues := contracts.AnyMapNode(values["image"])
			if _, exists := imageValues["endpointPath"]; exists {
				return nil, fmt.Errorf("load model %s: image.endpointPath is no longer supported; configure image.generation.endpointPath", entry.Name())
			}
			if _, exists := imageValues["endpoint-path"]; exists {
				return nil, fmt.Errorf("load model %s: image.endpoint-path is no longer supported; configure image.generation.endpointPath", entry.Name())
			}
			if err := ValidateModelImageConfig(model.Image); err != nil {
				return nil, fmt.Errorf("load model %s: image: %w", entry.Name(), err)
			}
		}
		result[key] = model
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
		BatchSize:    intNode(values["batchSize"]),
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
		Timeout:         intNode(values["timeout"]),
		DefaultSize:     strings.TrimSpace(contracts.FirstNonEmptyString(values["defaultSize"], values["default-size"])),
		ResponseFormats: firstStringSliceNode(values["responseFormats"], values["response-formats"]),
		Generation:      loadModelImageGeneration(values["generation"]),
		Edit:            loadModelImageEdit(values["edit"]),
	}
}

func loadModelImageGeneration(raw any) ModelImageGenerationConfig {
	if raw == nil {
		return ModelImageGenerationConfig{}
	}
	values := contracts.AnyMapNode(raw)
	return ModelImageGenerationConfig{
		EndpointPath:       strings.TrimSpace(contracts.FirstNonEmptyString(values["endpointPath"], values["endpoint-path"])),
		RequestFormat:      strings.ToLower(strings.TrimSpace(contracts.FirstNonEmptyString(values["requestFormat"], values["request-format"]))),
		OmitResponseFormat: parseTruthyDefault(values["omitResponseFormat"], false),
	}
}

func loadModelImageEdit(raw any) ModelImageEditConfig {
	if raw == nil {
		return ModelImageEditConfig{}
	}
	values := contracts.AnyMapNode(raw)
	maskProtocol := strings.ToLower(strings.TrimSpace(contracts.FirstNonEmptyString(values["maskProtocol"], values["mask-protocol"])))
	if maskProtocol == "" {
		maskProtocol = ImageMaskProtocolNone
	}
	return ModelImageEditConfig{
		EndpointPath:       strings.TrimSpace(contracts.FirstNonEmptyString(values["endpointPath"], values["endpoint-path"])),
		RequestFormat:      strings.ToLower(strings.TrimSpace(contracts.FirstNonEmptyString(values["requestFormat"], values["request-format"]))),
		OmitResponseFormat: parseTruthyDefault(values["omitResponseFormat"], false),
		MaskProtocol:       maskProtocol,
		Configured:         true,
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

func ParseModelPricing(raw any) ModelPricing {
	values := contracts.AnyMapNode(raw)
	if len(values) == 0 {
		return ModelPricing{}
	}
	return ModelPricing{
		Currency:       strings.ToUpper(strings.TrimSpace(stringNode(values["currency"]))),
		Unit:           strings.ToLower(strings.TrimSpace(stringNode(values["unit"]))),
		InputCacheHit:  floatNode(values["inputCacheHit"]),
		InputCacheMiss: floatNode(values["inputCacheMiss"]),
		Output:         floatNode(values["output"]),
		Characters:     floatNode(values["characters"]),
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
