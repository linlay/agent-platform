package knowledge

import (
	"strings"
	"testing"
)

func TestParseConfigDefaultsAndCanonicalFields(t *testing.T) {
	defaults, err := ParseConfig(nil)
	if err != nil {
		t.Fatalf("parse defaults: %v", err)
	}
	if defaults.Enabled || defaults.LibraryID != "" || defaults.Retrieval.TopK != 8 || defaults.Retrieval.CandidateFloor != 30 || defaults.Retrieval.CandidateMultiplier != 4 || defaults.Retrieval.CandidateMax != 500 {
		t.Fatalf("unexpected defaults: %+v", defaults)
	}

	canonical, err := ParseConfig(map[string]any{"libraryId": "research", "retrieval": map[string]any{"topK": 12, "candidateFloor": 24, "candidateMultiplier": 5, "candidateMax": 240}})
	if err != nil || !canonical.Enabled || canonical.LibraryID != "research" || canonical.Retrieval.TopK != 12 {
		t.Fatalf("binding: %+v %v", canonical, err)
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
	if err := ValidateConfig(valid); err != nil {
		t.Fatalf("default retrieval must remain valid: %v", err)
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

func TestRejectRemovedAgentIndexSettings(t *testing.T) {
	for _, key := range []string{"enabled", "storage", "chunk", "include", "exclude", "tags", "embedding"} {
		if _, err := ParseConfig(map[string]any{key: true}); err == nil || !strings.Contains(err.Error(), "libraryId") {
			t.Fatalf("%s: %v", key, err)
		}
	}
	for _, id := range []string{"libraries", "../docs", "Docs", ""} {
		if _, err := ParseConfig(map[string]any{"libraryId": id}); err == nil {
			t.Fatal("bad ID", id)
		}
	}
}
