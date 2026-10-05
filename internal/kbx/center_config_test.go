package kbx

import (
	"agent-platform/internal/models"
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
	os.WriteFile(filepath.Join(root, "models", "embed.yml"), []byte("key: embed\nprovider: test\ntype: embedding\nmodelId: text-embedding-v4\nembedding:\n  dimension: 1024\n  endpointPath: /v1/embeddings\n"), 0600)
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
				Model  string
				Prompt string
				URL    string
			}
		}
	}
	if err = json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Models.Embedding.Model != "text-embedding-v4" || cfg.Models.Embedding.Prompt != "raw" || cfg.Models.Embedding.URL != "https://example.test/v1/embeddings" {
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
