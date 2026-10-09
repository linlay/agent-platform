package kbx

import (
	"agent-platform/internal/kbasescenter"
	"agent-platform/internal/knowledge"
	"agent-platform/internal/models"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGlobalCenterConfigUsesSelectedRegistryModel(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"models", "providers"} {
		os.Mkdir(filepath.Join(root, dir), 0700)
	}
	os.WriteFile(filepath.Join(root, "providers", "test.yml"), []byte("key: test\nbaseUrl: https://example.test\napiKey: private-test-key\n"), 0600)
	os.WriteFile(filepath.Join(root, "models", "embed.yml"), []byte("key: embed\nprovider: test\ntype: embedding\nmodelId: text-embedding-v4\nembedding:\n  dimension: 1024\n  batchSize: 20\n  endpointPath: /v1/embeddings\n"), 0600)
	registry, err := models.LoadModelRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "state", "kbx", "index.yml")
	engine, err := NewConfiguredCenterEngine(file, registry, "embed", "raw")
	if err != nil {
		t.Fatal(err)
	}
	if !engine.embedding || engine.runner.(cliRunner).configFile != file {
		t.Fatal("global file not wired")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Models struct {
			Embedding struct {
				BatchSize int `json:"batch_size"`
				Model     string
				Prompt    string
				URL       string
			}
		}
	}
	if err = json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Models.Embedding.BatchSize != 20 || cfg.Models.Embedding.Model != "text-embedding-v4" || cfg.Models.Embedding.Prompt != "raw" || cfg.Models.Embedding.URL != "https://example.test/v1/embeddings" {
		t.Fatal("wrong model snapshot")
	}
	st, _ := os.Stat(file)
	if st.Mode().Perm() != 0600 {
		t.Fatal("config permissions")
	}
	if _, err = NewConfiguredCenterEngine(file, registry, "missing", "raw"); err == nil {
		t.Fatal("missing model accepted")
	}
	after, _ := os.ReadFile(file)
	if string(raw) != string(after) {
		t.Fatal("failed publish replaced config")
	}
}

func TestSharedModelSourceIgnoresAgentOverrideAndRetainsChunking(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"models", "providers"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	provider := filepath.Join(root, "providers", "test.yml")
	if err := os.WriteFile(provider, []byte("key: test\nbaseUrl: https://first.example\napiKey: fixture-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "models", "embed.yml"), []byte("key: embed\nprovider: test\ntype: embedding\nmodelId: embedding-fixture\nembedding:\n  dimension: 8\n"), 0600); err != nil {
		t.Fatal(err)
	}
	registry, err := models.LoadModelRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	source := &ModelConfigSource{File: filepath.Join(root, "state", "index.yml"), Registry: registry, ModelKey: "embed", Prompt: "qwen3"}
	m := NewManager(Options{ConfigSource: source}, nil, registry)
	spec := knowledge.DefaultConfig()
	chunk := knowledge.ChunkConfig{Unit: "chars", MaxChars: 800, OverlapChars: 80}
	raw, err := m.config(library{spec: knowledge.AgentSpec{Config: spec}, source: kbasescenter.Collection{Chunk: chunk}}, true)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err = json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	role := cfg["models"].(map[string]any)["embedding"].(map[string]any)
	if role["model"] != "embedding-fixture" || role["prompt"] != "qwen3" || cfg["chunking"].(map[string]any)["max_chars"] != float64(800) {
		t.Fatal("source or chunking changed")
	}
	before, _ := os.Stat(source.File)
	if _, err = source.Snapshot(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(source.File)
	if !os.SameFile(before, after) {
		t.Fatal("unchanged snapshot was rewritten")
	}
	if err = os.WriteFile(provider, []byte("key: test\nbaseUrl: https://second.example\napiKey: rotated-fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = registry.ReloadProviders(); err != nil {
		t.Fatal(err)
	}
	updated, err := source.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(updated, []byte("https://second.example")) {
		t.Fatal("registry update not synchronized")
	}
	if _, err = os.Stat(source.File); err != nil {
		t.Fatal(err)
	}
}
