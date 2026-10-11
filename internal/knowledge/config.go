package knowledge

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

const ChunkUnitChars = "chars"

// Config is the effective per-agent KBASE configuration.
type Config struct {
	LibraryID          string
	Enabled            bool // Derived from LibraryID, never a YAML field.
	Retrieval          RetrievalConfig
	RetrievalOverrides *RetrievalSettings `json:"-"`
}

type ChunkConfig struct {
	Strategy     string `json:"strategy,omitempty"`
	Unit         string `json:"unit,omitempty"`
	MaxChars     int    `json:"maxChars,omitempty"`
	OverlapChars int    `json:"overlapChars,omitempty"`
}

type RetrievalConfig struct {
	MinScore            *float64 `json:"minScore,omitempty"`
	RecencyWeight       *float64 `json:"recencyWeight,omitempty"`
	RecencyHalfLifeDays *float64 `json:"recencyHalfLifeDays,omitempty"`
	Rerank              *bool    `json:"rerank,omitempty"`
	QueryExpansion      *bool    `json:"queryExpansion,omitempty"`
	TopK                int      `json:"topK"`
	CandidateFloor      int      `json:"candidateFloor"`
	CandidateMultiplier int      `json:"candidateMultiplier"`
	CandidateMax        int      `json:"candidateMax"`
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
	return ChunkConfig{Unit: ChunkUnitChars, Strategy: "window", MaxChars: 3600, OverlapChars: 540}
}

func DefaultConfig() Config {
	return Config{
		Retrieval: RetrievalConfig{
			TopK:                8,
			CandidateFloor:      30,
			CandidateMultiplier: 4,
			CandidateMax:        500,
		},
	}
}

func ParseConfig(node map[string]any) (Config, error) {
	if err := ValidateConfigSchema(node); err != nil {
		return Config{}, err
	}
	cfg := DefaultConfig()
	if len(node) == 0 {
		return cfg, nil
	}
	cfg.LibraryID = anyString(node["libraryId"])
	cfg.Enabled = cfg.LibraryID != ""

	settings, err := ParseRetrievalSettings(anyMap(node["retrieval"]))
	if err != nil {
		return cfg, err
	}
	cfg.RetrievalOverrides = &settings
	cfg.Retrieval = settings.Apply(cfg.Retrieval)
	return cfg, nil
}

var libraryIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func ValidateConfigSchema(node map[string]any) error {
	for key := range node {
		if key != "libraryId" && key != "retrieval" {
			return fmt.Errorf("kbaseConfig.%s was removed; bind libraryId and configure source filters/chunking in kbases/<id>/library.yml, embedding in library models (runtime.kbx supplies defaults)", key)
		}
	}
	if len(node) == 0 {
		return nil
	}
	id, ok := node["libraryId"].(string)
	if !ok || !libraryIDPattern.MatchString(id) || id == "libraries" {
		return fmt.Errorf("kbaseConfig.libraryId must be a valid library ID (libraries is reserved)")
	}
	if raw, exists := node["retrieval"]; exists {
		if _, ok := raw.(map[string]any); !ok {
			return fmt.Errorf("kbaseConfig.retrieval must be a mapping")
		}
	}
	_, err := ParseRetrievalSettings(anyMap(node["retrieval"]))
	if err != nil {
		return fmt.Errorf("kbaseConfig.%w", err)
	}
	return nil
}

func ValidateConfig(cfg Config) error {
	if cfg.RetrievalOverrides != nil {
		return ValidateRetrievalSettings(*cfg.RetrievalOverrides)
	}
	return ValidateRetrieval(cfg.Retrieval)
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

func anyInt(value any) int {
	out, _ := parseConfigInt(value)
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

func applyIntAliases(values map[string]any, target *int, keys ...string) {
	for _, key := range keys {
		if value, exists := values[key]; exists {
			*target = anyInt(value)
		}
	}
}
