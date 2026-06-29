package kbase

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/models"
)

type stubRegistry struct {
	agents map[string]catalog.AgentDefinition
}

func (r stubRegistry) Agents(string) []api.AgentSummary {
	out := make([]api.AgentSummary, 0, len(r.agents))
	for _, def := range r.agents {
		out = append(out, api.AgentSummary{Key: def.Key, Mode: catalog.AgentModeForAPI(def.Mode)})
	}
	return out
}

func (r stubRegistry) Teams() []api.TeamSummary         { return nil }
func (r stubRegistry) Skills(string) []api.SkillSummary { return nil }
func (r stubRegistry) SkillDefinition(string) (catalog.SkillDefinition, bool) {
	return catalog.SkillDefinition{}, false
}
func (r stubRegistry) Tools(string, string) []api.ToolSummary { return nil }
func (r stubRegistry) Tool(string) (api.ToolDetailResponse, bool) {
	return api.ToolDetailResponse{}, false
}
func (r stubRegistry) DefaultAgentKey() string { return "" }
func (r stubRegistry) AgentDefinition(key string) (catalog.AgentDefinition, bool) {
	def, ok := r.agents[key]
	return def, ok
}
func (r stubRegistry) TeamDefinition(string) (catalog.TeamDefinition, bool) {
	return catalog.TeamDefinition{}, false
}
func (r stubRegistry) Reload(context.Context, string) error { return nil }

func TestManagerRefreshSearchReadAndIgnoreKBaseDir(t *testing.T) {
	embeddingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode embedding request: %v", err)
		}
		resp := map[string]any{"data": []map[string]any{}}
		data := resp["data"].([]map[string]any)
		for i, text := range req.Input {
			lower := strings.ToLower(text)
			vector := []float64{0, 0, 1}
			if strings.Contains(lower, "alpha") {
				vector[0] = 1
			}
			if strings.Contains(lower, "beta") {
				vector[1] = 1
			}
			data = append(data, map[string]any{"index": i, "embedding": vector})
		}
		resp["data"] = data
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer embeddingServer.Close()

	root := t.TempDir()
	registriesDir := filepath.Join(root, "registries")
	providersDir := filepath.Join(registriesDir, "providers")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatalf("mkdir providers: %v", err)
	}
	if err := os.WriteFile(filepath.Join(providersDir, "mock.yml"), []byte(strings.Join([]string{
		"key: mock",
		"baseUrl: " + embeddingServer.URL,
		"apiKey: test-key",
		"embedding:",
		"  model: mock-embedding",
		"  dimension: 3",
		"  timeout: 5",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write provider: %v", err)
	}
	modelRegistry, err := models.LoadModelRegistry(registriesDir)
	if err != nil {
		t.Fatalf("load model registry: %v", err)
	}

	workspace := filepath.Join(root, "docs")
	if err := os.MkdirAll(filepath.Join(workspace, ".kbase"), 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "alpha.md"), []byte("# Alpha\nalpha overview"), 0o644); err != nil {
		t.Fatalf("write alpha: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "beta.txt"), []byte("beta reference material"), 0o644); err != nil {
		t.Fatalf("write beta: %v", err)
	}
	deck := zipFixture(t, map[string]string{
		"ppt/slides/slide1.xml": `<?xml version="1.0" encoding="UTF-8"?>
<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
  <p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>gamma slide insight</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld>
</p:sld>`,
	})
	if err := os.WriteFile(filepath.Join(workspace, "deck.pptx"), deck, 0o644); err != nil {
		t.Fatalf("write deck: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".kbase", "hidden.md"), []byte("hidden beta"), 0o644); err != nil {
		t.Fatalf("write hidden: %v", err)
	}
	def := catalog.AgentDefinition{
		Key:       "docs",
		Mode:      catalog.AgentModeKBase,
		Workspace: catalog.AgentWorkspaceConfig{Root: workspace},
		KBaseConfig: catalog.AgentKBaseConfig{
			Embedding: catalog.AgentKBaseEmbeddingConfig{ProviderKey: "mock"},
			Storage:   catalog.AgentKBaseStorageConfig{Location: "runtime"},
			Include:   []string{"**/*.md", "**/*.txt", "**/*.pptx"},
			Exclude:   []string{".git/**", ".kbase/**", "node_modules/**"},
			Chunk:     catalog.AgentKBaseChunkConfig{MaxChars: 4000, OverlapChars: 600},
			Retrieval: catalog.AgentKBaseRetrievalConfig{TopK: 5, VectorWeight: 0.7, FTSWeight: 0.3},
		},
	}
	cfg := config.Config{}
	cfg.Paths.KBaseDir = filepath.Join(root, "kbase")
	manager := NewManager(cfg, stubRegistry{agents: map[string]catalog.AgentDefinition{"docs": def}}, modelRegistry)

	refresh, err := manager.Refresh(context.Background(), "docs", RefreshOptions{Mode: "manual"})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if refresh.Status != "success" || refresh.ScannedFiles != 3 {
		t.Fatalf("unexpected refresh result: %#v", refresh)
	}
	status, err := manager.Status("docs")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Files != 3 || status.Chunks == 0 || status.Stale {
		t.Fatalf("unexpected status: %#v", status)
	}
	search, err := manager.Search(context.Background(), "docs", "beta", SearchOptions{Limit: 3})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if search.Count == 0 || search.Results[0].Path != "beta.txt" {
		t.Fatalf("expected beta.txt top hit, got %#v", search)
	}
	read, err := manager.Read("docs", ReadOptions{ChunkID: search.Results[0].ChunkID})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !read.Found || !strings.Contains(read.Content, "beta reference") {
		t.Fatalf("unexpected read result: %#v", read)
	}
	slideSearch, err := manager.Search(context.Background(), "docs", "gamma", SearchOptions{Limit: 3})
	if err != nil {
		t.Fatalf("slide search: %v", err)
	}
	if slideSearch.Count == 0 || slideSearch.Results[0].Path != "deck.pptx" || slideSearch.Results[0].SlideStart != 1 || slideSearch.Results[0].SourceType != "pptx" {
		t.Fatalf("expected deck.pptx slide hit, got %#v", slideSearch)
	}
	slideRead, err := manager.Read("docs", ReadOptions{ChunkID: slideSearch.Results[0].ChunkID})
	if err != nil {
		t.Fatalf("slide read: %v", err)
	}
	if !slideRead.Found || slideRead.SlideStart != 1 || slideRead.SourceType != "pptx" || !strings.Contains(slideRead.Content, "gamma slide") {
		t.Fatalf("unexpected slide read result: %#v", slideRead)
	}
}
