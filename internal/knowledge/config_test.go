package knowledge

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseConfigDefaultsAndCanonicalFields(t *testing.T) {
	defaults, err := ParseConfig(nil)
	if err != nil {
		t.Fatalf("parse defaults: %v", err)
	}
	if defaults.Storage.Location != "runtime" ||
		defaults.Chunk.Unit != ChunkUnitEstimatedTokens ||
		defaults.Chunk.MaxTokens != 1000 ||
		defaults.Chunk.OverlapTokens != 100 ||
		defaults.Retrieval.TopK != 8 ||
		defaults.Retrieval.Fusion != RetrievalFusionRRF ||
		defaults.Retrieval.RRFK != 60 ||
		defaults.Retrieval.VectorWeight != 0.7 ||
		defaults.Retrieval.FTSWeight != 0.3 ||
		defaults.Retrieval.CandidateFloor != 30 ||
		defaults.Retrieval.CandidateMultiplier != 4 ||
		defaults.Retrieval.CandidateMax != 500 {
		t.Fatalf("unexpected defaults: %#v", defaults)
	}
	if !reflect.DeepEqual(defaults.Include, DefaultIncludePatterns()) || !reflect.DeepEqual(defaults.Exclude, DefaultExcludePatterns()) {
		t.Fatalf("unexpected default scope: %#v", defaults)
	}

	canonical, err := ParseConfig(map[string]any{
		"chunk": map[string]any{"unit": "chars", "maxChars": 3200, "overlapChars": 320},
		"retrieval": map[string]any{
			"topK":                12,
			"fusion":              "RRF",
			"rrfK":                42,
			"vectorWeight":        0.6,
			"ftsWeight":           0.4,
			"candidateFloor":      24,
			"candidateMultiplier": 5,
			"candidateMax":        240,
		},
	})
	if err != nil {
		t.Fatalf("parse canonical config: %v", err)
	}
	if canonical.Chunk.Unit != ChunkUnitChars || canonical.Chunk.MaxChars != 3200 || canonical.Chunk.OverlapChars != 320 ||
		canonical.Chunk.MaxTokens != 0 || canonical.Chunk.OverlapTokens != 0 {
		t.Fatalf("canonical char config changed: %#v", canonical.Chunk)
	}
	if canonical.Retrieval.TopK != 12 || canonical.Retrieval.Fusion != RetrievalFusionRRF || canonical.Retrieval.RRFK != 42 ||
		canonical.Retrieval.VectorWeight != 0.6 || canonical.Retrieval.FTSWeight != 0.4 ||
		canonical.Retrieval.CandidateFloor != 24 || canonical.Retrieval.CandidateMultiplier != 5 || canonical.Retrieval.CandidateMax != 240 {
		t.Fatalf("canonical retrieval changed: %#v", canonical.Retrieval)
	}
	for _, legacy := range []map[string]any{
		{"chunk": map[string]any{"maxChars": 3200}},
		{"chunk": map[string]any{"unit": "tokens", "maxTokens": 3200}},
		{"chunk": map[string]any{"unit": "characters", "maxChars": 3200}},
		{"chunk": map[string]any{"unit": "runes", "maxChars": 3200}},
	} {
		if _, err := ParseConfig(legacy); err == nil {
			t.Fatalf("legacy config must fail: %#v", legacy)
		}
	}
	for _, key := range []string{"top-k", "rrf-k", "vector-weight", "fts-weight", "candidate-floor", "candidate-multiplier", "candidate-max"} {
		if _, err := ParseConfig(map[string]any{"retrieval": map[string]any{key: 1}}); err == nil {
			t.Fatalf("kebab-case retrieval key %q must fail", key)
		}
	}
}

func TestValidateRetrievalConfig(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RetrievalConfig)
	}{
		{name: "topK zero", mutate: func(cfg *RetrievalConfig) { cfg.TopK = 0 }},
		{name: "topK too high", mutate: func(cfg *RetrievalConfig) { cfg.TopK = 51 }},
		{name: "unsupported fusion", mutate: func(cfg *RetrievalConfig) { cfg.Fusion = "linear" }},
		{name: "rrfK zero", mutate: func(cfg *RetrievalConfig) { cfg.RRFK = 0 }},
		{name: "rrfK too high", mutate: func(cfg *RetrievalConfig) { cfg.RRFK = 1001 }},
		{name: "negative vector weight", mutate: func(cfg *RetrievalConfig) { cfg.VectorWeight = -0.1 }},
		{name: "both weights zero", mutate: func(cfg *RetrievalConfig) { cfg.VectorWeight, cfg.FTSWeight = 0, 0 }},
		{name: "candidate floor below topK", mutate: func(cfg *RetrievalConfig) { cfg.CandidateFloor = cfg.TopK - 1 }},
		{name: "candidate multiplier zero", mutate: func(cfg *RetrievalConfig) { cfg.CandidateMultiplier = 0 }},
		{name: "candidate max below floor", mutate: func(cfg *RetrievalConfig) { cfg.CandidateMax = cfg.CandidateFloor - 1 }},
		{name: "candidate max too high", mutate: func(cfg *RetrievalConfig) { cfg.CandidateMax = 2001 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := DefaultConfig()
			test.mutate(&cfg.Retrieval)
			if err := ValidateConfig(cfg); err == nil {
				t.Fatalf("expected invalid retrieval config: %#v", cfg.Retrieval)
			}
		})
	}

	valid := DefaultConfig()
	valid.Retrieval.VectorWeight = 0
	if err := ValidateConfig(valid); err != nil {
		t.Fatalf("one zero weight must remain valid: %v", err)
	}
}

func TestValidateConfigSchemaRejectsRemovedEmbeddingFields(t *testing.T) {
	for _, key := range []string{"providerKey", "model", "dimension", "timeout"} {
		_, err := ParseConfig(map[string]any{
			"embedding": map[string]any{key: "legacy"},
		})
		if err == nil {
			t.Fatalf("expected removed embedding field %q to fail", key)
		}
	}
	if _, err := ParseConfig(map[string]any{"chunk": map[string]any{"unit": "bytes"}}); err == nil {
		t.Fatal("expected invalid chunk unit to fail")
	}
	for key, value := range map[string]any{
		"topK":         2.5,
		"rrfK":         "invalid",
		"vectorWeight": "NaN",
		"ftsWeight":    "invalid",
	} {
		if _, err := ParseConfig(map[string]any{"retrieval": map[string]any{key: value}}); err == nil {
			t.Fatalf("expected invalid retrieval field %q to fail", key)
		}
	}
}

func TestParseConfigReadsPublicTags(t *testing.T) {
	cfg, err := ParseConfig(map[string]any{
		"tags": []any{"售后", "退款"},
	})
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if strings.Join(cfg.Tags, ",") != "售后,退款" {
		t.Fatalf("unexpected tags %#v", cfg.Tags)
	}
	if _, err := ParseConfig(map[string]any{"tags": []any{"售后", 42}}); err == nil {
		t.Fatal("expected non-string public tag to fail")
	}
}
