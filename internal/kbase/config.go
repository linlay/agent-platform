package kbase

import (
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"agent-platform/internal/deprecation"
)

const (
	ChunkUnitChars           = "chars"
	ChunkUnitEstimatedTokens = "estimatedTokens"
	WorkspaceRootChat        = "@chat"
	RetrievalFusionRRF       = "rrf"
)

type AgentConfig struct {
	Enabled    bool
	EnabledSet bool
	Source     SourceConfig
	Tags       []string
	Embedding  EmbeddingConfig
	Storage    StorageConfig
	Include    []string
	Exclude    []string
	Chunk      ChunkConfig
	Retrieval  RetrievalConfig
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
	TopK                int     `json:"topK"`
	Fusion              string  `json:"fusion"`
	RRFK                int     `json:"rrfK"`
	VectorWeight        float64 `json:"vectorWeight"`
	FTSWeight           float64 `json:"ftsWeight"`
	CandidateFloor      int     `json:"candidateFloor"`
	CandidateMultiplier int     `json:"candidateMultiplier"`
	CandidateMax        int     `json:"candidateMax"`
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

func DefaultAgentConfig() AgentConfig {
	return AgentConfig{
		Storage: StorageConfig{Location: "runtime"},
		Include: DefaultIncludePatterns(),
		Exclude: DefaultExcludePatterns(),
		Chunk:   DefaultChunkConfig(),
		Retrieval: RetrievalConfig{
			TopK:                8,
			Fusion:              RetrievalFusionRRF,
			RRFK:                60,
			VectorWeight:        0.7,
			FTSWeight:           0.3,
			CandidateFloor:      30,
			CandidateMultiplier: 4,
			CandidateMax:        500,
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
	if rawEnabled, exists := node["enabled"]; exists {
		enabled, ok := rawEnabled.(bool)
		if !ok {
			return AgentConfig{}, fmt.Errorf("kbaseConfig.enabled must be a boolean")
		}
		cfg.Enabled = enabled
		cfg.EnabledSet = true
	}
	source := anyMap(node["source"])
	cfg.Source = SourceConfig{Root: strings.TrimSpace(anyString(source["root"]))}
	cfg.Tags = anyStrings(node["tags"])
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
	applyIntAliases(retrieval, &cfg.Retrieval.TopK, "topK")
	applyStringAliases(retrieval, &cfg.Retrieval.Fusion, "fusion")
	applyIntAliases(retrieval, &cfg.Retrieval.RRFK, "rrfK")
	applyFloatAliases(retrieval, &cfg.Retrieval.VectorWeight, "vectorWeight")
	applyFloatAliases(retrieval, &cfg.Retrieval.FTSWeight, "ftsWeight")
	applyIntAliases(retrieval, &cfg.Retrieval.CandidateFloor, "candidateFloor")
	applyIntAliases(retrieval, &cfg.Retrieval.CandidateMultiplier, "candidateMultiplier")
	applyIntAliases(retrieval, &cfg.Retrieval.CandidateMax, "candidateMax")
	return cfg, nil
}

func ParseChunkConfig(value any) ChunkConfig {
	chunk := anyMap(value)
	if len(chunk) == 0 {
		return DefaultChunkConfig()
	}
	cfg := DefaultChunkConfig()
	rawUnit, hasUnit := firstExisting(chunk, "unit")
	unit := strings.TrimSpace(anyString(rawUnit))
	if hasUnit {
		cfg.Unit = unit
		if cfg.Unit == ChunkUnitChars {
			cfg.MaxChars = 4000
			cfg.OverlapChars = 600
			cfg.MaxTokens = 0
			cfg.OverlapTokens = 0
		}
	}
	if maxTokens := anyInt(firstAny(chunk, "maxTokens")); maxTokens > 0 {
		cfg.MaxTokens = maxTokens
	}
	if _, exists := firstExisting(chunk, "overlapTokens"); exists {
		if overlapTokens := anyInt(firstAny(chunk, "overlapTokens")); overlapTokens >= 0 {
			cfg.OverlapTokens = overlapTokens
		}
	}
	if maxChars := anyInt(firstAny(chunk, "maxChars")); maxChars > 0 {
		cfg.MaxChars = maxChars
	}
	if _, exists := firstExisting(chunk, "overlapChars"); exists {
		if overlapChars := anyInt(firstAny(chunk, "overlapChars")); overlapChars >= 0 {
			cfg.OverlapChars = overlapChars
		}
	}
	return NormalizeChunkConfig(cfg)
}

func NormalizeChunkConfig(cfg ChunkConfig) ChunkConfig {
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
	switch strings.TrimSpace(value) {
	case "", ChunkUnitEstimatedTokens:
		return ChunkUnitEstimatedTokens, true
	case ChunkUnitChars:
		return ChunkUnitChars, true
	default:
		return "", false
	}
}

func ValidateAgentConfigSchema(node map[string]any) error {
	if rawEnabled, exists := node["enabled"]; exists {
		if _, ok := rawEnabled.(bool); !ok {
			return fmt.Errorf("kbaseConfig.enabled must be a boolean")
		}
	}
	if rawSource, exists := node["source"]; exists {
		var source map[string]any
		switch rawSource.(type) {
		case map[string]any, map[any]any:
			source = anyMap(rawSource)
		default:
			if rawSource != nil {
				return fmt.Errorf("kbaseConfig.source must be a map")
			}
		}
		if rawRoot, exists := source["root"]; exists {
			if _, ok := rawRoot.(string); !ok {
				return fmt.Errorf("kbaseConfig.source.root must be a string")
			}
		}
	}
	if rawTags, exists := node["tags"]; exists {
		switch tags := rawTags.(type) {
		case []string:
		case []any:
			for _, tag := range tags {
				if _, ok := tag.(string); !ok {
					return fmt.Errorf("kbaseConfig.tags must contain only strings")
				}
			}
		default:
			return fmt.Errorf("kbaseConfig.tags must be a list of strings")
		}
	}
	embedding := anyMap(node["embedding"])
	for _, key := range []string{"providerKey", "model", "dimension", "timeout"} {
		if _, exists := embedding[key]; exists {
			return fmt.Errorf("kbaseConfig.embedding.%s is no longer supported; use kbaseConfig.embedding.modelKey", key)
		}
	}
	chunk := anyMap(node["chunk"])
	if len(chunk) > 0 {
		if _, exists := chunk["unit"]; !exists {
			if _, hasChars := chunk["maxChars"]; hasChars {
				return deprecation.New("kbaseConfig.chunk.unit is required when maxChars or overlapChars is configured; use unit: chars")
			}
			if _, hasOverlap := chunk["overlapChars"]; hasOverlap {
				return deprecation.New("kbaseConfig.chunk.unit is required when maxChars or overlapChars is configured; use unit: chars")
			}
		}
	}
	for _, key := range []string{"max-chars", "overlap-chars", "max-tokens", "overlap-tokens"} {
		if _, exists := chunk[key]; exists {
			return deprecation.New("kbaseConfig.chunk.%s was removed; use camelCase", key)
		}
	}
	if rawUnit, exists := chunk["unit"]; exists {
		if _, ok := NormalizeChunkUnit(anyString(rawUnit)); !ok || strings.TrimSpace(anyString(rawUnit)) == "" {
			return fmt.Errorf("kbaseConfig.chunk.unit must be estimatedTokens or chars")
		}
	}
	retrieval := anyMap(node["retrieval"])
	for _, key := range []string{"top-k", "rrf-k", "vector-weight", "fts-weight", "candidate-floor", "candidate-multiplier", "candidate-max"} {
		if _, exists := retrieval[key]; exists {
			return deprecation.New("kbaseConfig.retrieval.%s was removed; use camelCase", key)
		}
	}
	for _, key := range []string{"topK", "rrfK", "candidateFloor", "candidateMultiplier", "candidateMax"} {
		if value, exists := retrieval[key]; exists {
			if _, ok := parseConfigInt(value); !ok {
				return fmt.Errorf("kbaseConfig.retrieval.%s must be an integer", key)
			}
		}
	}
	for _, key := range []string{"vectorWeight", "ftsWeight"} {
		if value, exists := retrieval[key]; exists {
			if _, ok := parseConfigFloat(value); !ok {
				return fmt.Errorf("kbaseConfig.retrieval.%s must be a finite number", key)
			}
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
	if cfg.Retrieval.TopK < 1 || cfg.Retrieval.TopK > 50 {
		return fmt.Errorf("kbaseConfig.retrieval.topK must be between 1 and 50")
	}
	if !strings.EqualFold(strings.TrimSpace(cfg.Retrieval.Fusion), RetrievalFusionRRF) {
		return fmt.Errorf("kbaseConfig.retrieval.fusion must be rrf")
	}
	if cfg.Retrieval.RRFK < 1 || cfg.Retrieval.RRFK > 1000 {
		return fmt.Errorf("kbaseConfig.retrieval.rrfK must be between 1 and 1000")
	}
	if math.IsNaN(cfg.Retrieval.VectorWeight) || math.IsInf(cfg.Retrieval.VectorWeight, 0) ||
		math.IsNaN(cfg.Retrieval.FTSWeight) || math.IsInf(cfg.Retrieval.FTSWeight, 0) ||
		cfg.Retrieval.VectorWeight < 0 || cfg.Retrieval.FTSWeight < 0 ||
		(cfg.Retrieval.VectorWeight == 0 && cfg.Retrieval.FTSWeight == 0) {
		return fmt.Errorf("kbaseConfig.retrieval weights must be non-negative and not both zero")
	}
	if cfg.Retrieval.CandidateFloor < cfg.Retrieval.TopK {
		return fmt.Errorf("kbaseConfig.retrieval.candidateFloor must be at least topK")
	}
	if cfg.Retrieval.CandidateMultiplier < 1 {
		return fmt.Errorf("kbaseConfig.retrieval.candidateMultiplier must be at least 1")
	}
	if cfg.Retrieval.CandidateMax < cfg.Retrieval.CandidateFloor || cfg.Retrieval.CandidateMax > 2000 {
		return fmt.Errorf("kbaseConfig.retrieval.candidateMax must be between candidateFloor and 2000")
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
	out, _ := parseConfigInt(value)
	return out
}

func anyFloat(value any) float64 {
	out, _ := parseConfigFloat(value)
	return out
}

func parseConfigInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), int64(int(typed)) == typed
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || math.Trunc(typed) != typed {
			return 0, false
		}
		parsed := int(typed)
		return parsed, float64(parsed) == typed
	default:
		text := anyString(value)
		if text == "" {
			return 0, false
		}
		parsed, err := strconv.Atoi(text)
		return parsed, err == nil
	}
}

func parseConfigFloat(value any) (float64, bool) {
	var parsed float64
	switch typed := value.(type) {
	case float64:
		parsed = typed
	case float32:
		parsed = float64(typed)
	case int:
		parsed = float64(typed)
	case int64:
		parsed = float64(typed)
	default:
		text := anyString(value)
		if text == "" {
			return 0, false
		}
		var err error
		parsed, err = strconv.ParseFloat(text, 64)
		if err != nil {
			return 0, false
		}
	}
	return parsed, !math.IsNaN(parsed) && !math.IsInf(parsed, 0)
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

func applyIntAliases(values map[string]any, target *int, keys ...string) {
	for _, key := range keys {
		if value, exists := values[key]; exists {
			*target = anyInt(value)
		}
	}
}

func applyFloatAliases(values map[string]any, target *float64, keys ...string) {
	for _, key := range keys {
		if value, exists := values[key]; exists {
			*target = anyFloat(value)
		}
	}
}

func applyStringAliases(values map[string]any, target *string, keys ...string) {
	for _, key := range keys {
		if value, exists := values[key]; exists {
			*target = strings.ToLower(strings.TrimSpace(anyString(value)))
		}
	}
}
