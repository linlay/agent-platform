package kbase

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const (
	ChunkUnitChars           = "chars"
	ChunkUnitEstimatedTokens = "estimatedTokens"
	WorkspaceRootChat        = "@chat"
)

type AgentConfig struct {
	Embedding EmbeddingConfig
	Storage   StorageConfig
	Include   []string
	Exclude   []string
	Chunk     ChunkConfig
	Retrieval RetrievalConfig
}

type EmbeddingConfig struct {
	ModelKey string
}

type StorageConfig struct {
	Location string
}

type ChunkConfig struct {
	Unit          string `json:"unit,omitempty"`
	MaxChars      int    `json:"maxChars,omitempty"`
	OverlapChars  int    `json:"overlapChars,omitempty"`
	MaxTokens     int    `json:"maxTokens,omitempty"`
	OverlapTokens int    `json:"overlapTokens,omitempty"`
}

type RetrievalConfig struct {
	TopK         int
	VectorWeight float64
	FTSWeight    float64
}

type ExtractionConfig struct {
	Timeout      time.Duration
	MaxFileBytes int64
	PDF          PDFExtractionConfig
	DOCX         DOCXExtractionConfig
	PPTX         PPTXExtractionConfig
}

type PDFExtractionConfig struct {
	Enabled bool
	Backend string
	Binary  string
}

type DOCXExtractionConfig struct {
	Enabled bool
	Backend string
}

type PPTXExtractionConfig struct {
	Enabled      bool
	Backend      string
	IncludeNotes bool
}

type CreateDefaults struct {
	ModelKey          string
	ReasoningEffort   string
	EmbeddingModelKey string
}

func DefaultIncludePatterns() []string {
	return []string{
		"**/*.md",
		"**/*.txt",
		"**/*.html",
		"**/*.htm",
		"**/*.pdf",
		"**/*.docx",
		"**/*.pptx",
	}
}

func DefaultExcludePatterns() []string {
	return []string{".git/**", ".kbase/**", "node_modules/**"}
}

func DefaultChunkConfig() ChunkConfig {
	return ChunkConfig{
		Unit:          ChunkUnitEstimatedTokens,
		MaxTokens:     1000,
		OverlapTokens: 100,
	}
}

func defaultLegacyCharChunkConfig() ChunkConfig {
	return ChunkConfig{
		Unit:         ChunkUnitChars,
		MaxChars:     4000,
		OverlapChars: 600,
	}
}

func DefaultAgentConfig() AgentConfig {
	return AgentConfig{
		Storage: StorageConfig{Location: "runtime"},
		Include: DefaultIncludePatterns(),
		Exclude: DefaultExcludePatterns(),
		Chunk:   DefaultChunkConfig(),
		Retrieval: RetrievalConfig{
			TopK:         8,
			VectorWeight: 0.7,
			FTSWeight:    0.3,
		},
	}
}

func ParseAgentConfig(node map[string]any) (AgentConfig, error) {
	if err := ValidateAgentConfigSchema(node); err != nil {
		return AgentConfig{}, err
	}
	cfg := DefaultAgentConfig()
	if len(node) == 0 {
		return cfg, nil
	}
	embedding := anyMap(node["embedding"])
	cfg.Embedding = EmbeddingConfig{ModelKey: anyString(embedding["modelKey"])}
	storage := anyMap(node["storage"])
	if location := strings.ToLower(strings.TrimSpace(anyString(storage["location"]))); location != "" {
		cfg.Storage.Location = location
	}
	if include := anyStrings(node["include"]); len(include) > 0 {
		cfg.Include = include
	}
	if exclude := anyStrings(node["exclude"]); len(exclude) > 0 {
		cfg.Exclude = exclude
	}
	cfg.Chunk = ParseChunkConfig(node["chunk"])
	retrieval := anyMap(node["retrieval"])
	if topK := anyInt(retrieval["topK"]); topK > 0 {
		cfg.Retrieval.TopK = topK
	}
	if topK := anyInt(retrieval["top-k"]); topK > 0 {
		cfg.Retrieval.TopK = topK
	}
	if weight := anyFloat(retrieval["vectorWeight"]); weight > 0 {
		cfg.Retrieval.VectorWeight = weight
	}
	if weight := anyFloat(retrieval["vector-weight"]); weight > 0 {
		cfg.Retrieval.VectorWeight = weight
	}
	if weight := anyFloat(retrieval["ftsWeight"]); weight > 0 {
		cfg.Retrieval.FTSWeight = weight
	}
	if weight := anyFloat(retrieval["fts-weight"]); weight > 0 {
		cfg.Retrieval.FTSWeight = weight
	}
	return cfg, nil
}

func ParseChunkConfig(value any) ChunkConfig {
	chunk := anyMap(value)
	if len(chunk) == 0 {
		return DefaultChunkConfig()
	}
	rawUnit, hasUnit := firstExisting(chunk, "unit")
	unit := strings.TrimSpace(anyString(rawUnit))
	hasMaxTokens := hasAny(chunk, "maxTokens", "max-tokens")
	hasOverlapTokens := hasAny(chunk, "overlapTokens", "overlap-tokens")
	hasMaxChars := hasAny(chunk, "maxChars", "max-chars")
	hasOverlapChars := hasAny(chunk, "overlapChars", "overlap-chars")
	useLegacyChars := !hasUnit && !hasMaxTokens && !hasOverlapTokens && (hasMaxChars || hasOverlapChars)

	cfg := DefaultChunkConfig()
	if useLegacyChars {
		cfg = defaultLegacyCharChunkConfig()
	}
	if hasUnit {
		if normalized, ok := NormalizeChunkUnit(unit); ok {
			cfg.Unit = normalized
		} else {
			cfg.Unit = unit
		}
		if cfg.Unit == ChunkUnitChars {
			cfg.MaxChars = 4000
			cfg.OverlapChars = 600
			cfg.MaxTokens = 0
			cfg.OverlapTokens = 0
		}
	}
	if maxTokens := anyInt(firstAny(chunk, "maxTokens", "max-tokens")); maxTokens > 0 {
		cfg.MaxTokens = maxTokens
		if !hasUnit {
			cfg.Unit = ChunkUnitEstimatedTokens
		}
	}
	if _, exists := firstExisting(chunk, "overlapTokens", "overlap-tokens"); exists {
		if overlapTokens := anyInt(firstAny(chunk, "overlapTokens", "overlap-tokens")); overlapTokens >= 0 {
			cfg.OverlapTokens = overlapTokens
			if !hasUnit {
				cfg.Unit = ChunkUnitEstimatedTokens
			}
		}
	}
	if maxChars := anyInt(firstAny(chunk, "maxChars", "max-chars")); maxChars > 0 {
		cfg.MaxChars = maxChars
		if !hasUnit && !hasMaxTokens && !hasOverlapTokens {
			cfg.Unit = ChunkUnitChars
		}
	}
	if _, exists := firstExisting(chunk, "overlapChars", "overlap-chars"); exists {
		if overlapChars := anyInt(firstAny(chunk, "overlapChars", "overlap-chars")); overlapChars >= 0 {
			cfg.OverlapChars = overlapChars
			if !hasUnit && !hasMaxTokens && !hasOverlapTokens {
				cfg.Unit = ChunkUnitChars
			}
		}
	}
	return NormalizeChunkConfig(cfg)
}

func NormalizeChunkConfig(cfg ChunkConfig) ChunkConfig {
	if strings.TrimSpace(cfg.Unit) == "" && cfg.MaxTokens <= 0 && cfg.OverlapTokens <= 0 && (cfg.MaxChars > 0 || cfg.OverlapChars > 0) {
		cfg.Unit = ChunkUnitChars
	}
	unit, ok := NormalizeChunkUnit(cfg.Unit)
	if !ok {
		unit = ChunkUnitEstimatedTokens
		if strings.TrimSpace(cfg.Unit) != "" {
			unit = strings.TrimSpace(cfg.Unit)
		}
	}
	cfg.Unit = unit
	switch cfg.Unit {
	case ChunkUnitChars:
		if cfg.MaxChars <= 0 {
			cfg.MaxChars = 4000
		}
		if cfg.OverlapChars < 0 {
			cfg.OverlapChars = 0
		}
		if cfg.OverlapChars >= cfg.MaxChars {
			cfg.OverlapChars = cfg.MaxChars / 5
		}
		cfg.MaxTokens = 0
		cfg.OverlapTokens = 0
	default:
		if cfg.MaxTokens <= 0 {
			cfg.MaxTokens = 1000
		}
		if cfg.OverlapTokens < 0 {
			cfg.OverlapTokens = 0
		}
		if cfg.OverlapTokens >= cfg.MaxTokens {
			cfg.OverlapTokens = cfg.MaxTokens / 5
		}
		cfg.MaxChars = 0
		cfg.OverlapChars = 0
	}
	return cfg
}

func NormalizeChunkUnit(value string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.NewReplacer("-", "", "_", "", " ", "").Replace(normalized)
	switch normalized {
	case "", "estimatedtokens", "tokens":
		return ChunkUnitEstimatedTokens, true
	case "chars", "characters", "runes":
		return ChunkUnitChars, true
	default:
		return "", false
	}
}

func ValidateAgentConfigSchema(node map[string]any) error {
	embedding := anyMap(node["embedding"])
	for _, key := range []string{"providerKey", "model", "dimension", "timeout"} {
		if _, exists := embedding[key]; exists {
			return fmt.Errorf("kbaseConfig.embedding.%s is no longer supported; use kbaseConfig.embedding.modelKey", key)
		}
	}
	chunk := anyMap(node["chunk"])
	if rawUnit, exists := chunk["unit"]; exists {
		if _, ok := NormalizeChunkUnit(anyString(rawUnit)); !ok {
			return fmt.Errorf("kbaseConfig.chunk.unit must be estimatedTokens or chars")
		}
	}
	return nil
}

func ValidateAgentConfig(cfg AgentConfig) error {
	switch strings.ToLower(strings.TrimSpace(cfg.Storage.Location)) {
	case "", "runtime", "workspace":
	default:
		return fmt.Errorf("kbaseConfig.storage.location must be runtime or workspace")
	}
	if _, ok := NormalizeChunkUnit(cfg.Chunk.Unit); !ok {
		return fmt.Errorf("kbaseConfig.chunk.unit must be estimatedTokens or chars")
	}
	return nil
}

func ValidateWorkspace(root string) error {
	root = strings.TrimSpace(root)
	if root == "" {
		return fmt.Errorf("runtimeConfig.workspaceRoot is required for mode: KBASE")
	}
	if strings.EqualFold(root, WorkspaceRootChat) {
		return fmt.Errorf("runtimeConfig.workspaceRoot for mode: KBASE must be an absolute path or ~/ path, not %q", WorkspaceRootChat)
	}
	// Catalog expands ~/ before mode validation, so at this boundary only an
	// absolute value is valid.
	if !filepath.IsAbs(root) {
		return fmt.Errorf("runtimeConfig.workspaceRoot for mode: KBASE must be an absolute path or ~/ path")
	}
	return nil
}

func ApplyCreateDefaults(definition map[string]any, defaults CreateDefaults) map[string]any {
	if definition == nil {
		return nil
	}
	out := cloneAnyMap(definition)
	if emptyAny(out["icon"]) {
		out["icon"] = map[string]any{"name": DefaultIconName}
	}
	visibility := cloneAnyMap(anyMap(out["visibility"]))
	if !hasNonBlankStrings(visibility["scopes"]) {
		visibility["scopes"] = []any{"nav"}
		out["visibility"] = visibility
	}
	modelKey := strings.TrimSpace(defaults.ModelKey)
	reasoningEffort := strings.TrimSpace(defaults.ReasoningEffort)
	if modelKey != "" || reasoningEffort != "" {
		modelConfig := cloneAnyMap(anyMap(out["modelConfig"]))
		if modelKey != "" && strings.TrimSpace(anyString(modelConfig["modelKey"])) == "" {
			modelConfig["modelKey"] = modelKey
		}
		if reasoningEffort != "" {
			reasoning := cloneAnyMap(anyMap(modelConfig["reasoning"]))
			if strings.TrimSpace(anyString(reasoning["effort"])) == "" {
				reasoning["effort"] = reasoningEffort
			}
			modelConfig["reasoning"] = reasoning
		}
		out["modelConfig"] = modelConfig
	}
	kbaseConfig := cloneAnyMap(anyMap(out["kbaseConfig"]))
	embedding := cloneAnyMap(anyMap(kbaseConfig["embedding"]))
	explicitModelKey := strings.TrimSpace(anyString(embedding["modelKey"]))
	if explicitModelKey != "" || strings.TrimSpace(defaults.EmbeddingModelKey) != "" {
		if explicitModelKey == "" {
			explicitModelKey = strings.TrimSpace(defaults.EmbeddingModelKey)
		}
		embedding["modelKey"] = explicitModelKey
		kbaseConfig["embedding"] = embedding
		out["kbaseConfig"] = kbaseConfig
	}
	return out
}

func anyMap(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return typed
	case map[any]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[fmt.Sprint(key)] = item
		}
		return out
	default:
		return nil
	}
}

func anyString(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func anyStrings(value any) []string {
	var raw []any
	switch typed := value.(type) {
	case []any:
		raw = typed
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if item = strings.TrimSpace(item); item != "" {
				out = append(out, item)
			}
		}
		return out
	default:
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if text := anyString(item); text != "" {
			out = append(out, text)
		}
	}
	return out
}

func anyInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		var out int
		_, _ = fmt.Sscan(anyString(value), &out)
		return out
	}
}

func anyFloat(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	default:
		var out float64
		_, _ = fmt.Sscan(anyString(value), &out)
		return out
	}
}

func firstExisting(values map[string]any, keys ...string) (any, bool) {
	for _, key := range keys {
		if value, exists := values[key]; exists {
			return value, true
		}
	}
	return nil, false
}

func firstAny(values map[string]any, keys ...string) any {
	value, _ := firstExisting(values, keys...)
	return value
}

func hasAny(values map[string]any, keys ...string) bool {
	_, ok := firstExisting(values, keys...)
	return ok
}

func cloneAnyMap(src map[string]any) map[string]any {
	if src == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		switch typed := value.(type) {
		case map[string]any:
			out[key] = cloneAnyMap(typed)
		case []any:
			out[key] = append([]any(nil), typed...)
		case []string:
			out[key] = append([]string(nil), typed...)
		default:
			out[key] = value
		}
	}
	return out
}

func emptyAny(value any) bool {
	if value == nil {
		return true
	}
	text, ok := value.(string)
	return ok && strings.TrimSpace(text) == ""
}

func hasNonBlankStrings(value any) bool {
	return len(anyStrings(value)) > 0
}
