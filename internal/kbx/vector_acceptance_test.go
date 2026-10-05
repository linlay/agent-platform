package kbx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/builtins"
	"agent-platform/internal/kbase"
	"agent-platform/internal/models"
)

type fixtureModels struct{ url string }

func (f fixtureModels) GetEmbedding(string) (models.ModelDefinition, models.ProviderDefinition, error) {
	return models.ModelDefinition{ModelID: "fixture", Embedding: models.ModelEmbeddingConfig{Dimension: 8, Timeout: 10, EndpointPath: "/embeddings"}}, models.ProviderDefinition{BaseURL: f.url, APIKey: "fixture-token"}, nil
}

// A local deterministic provider verifies the real CLI vector wire path and
// prefiltering, without transmitting user documents or claiming semantic quality.
func TestLiveVectorPrefilterAndLibraryIsolation(t *testing.T) {
	bin := os.Getenv("KBX_ACCEPTANCE_BIN")
	if bin == "" {
		t.Skip("set KBX_ACCEPTANCE_BIN")
	}
	t.Setenv("AP_BUILTINS_BIN", bin)
	if _, err := builtins.ConfigureProcessPath(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NO_PROXY", "127.0.0.1,localhost,::1")
	t.Setenv("no_proxy", "127.0.0.1,localhost,::1")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" || r.Header.Get("Authorization") != "Bearer fixture-token" {
			http.Error(w, "wrong configuration", 400)
			return
		}
		var in struct {
			Input json.RawMessage
			Model string
		}
		if e := json.NewDecoder(r.Body).Decode(&in); e != nil {
			http.Error(w, "invalid", 400)
			return
		}
		var texts []string
		if e := json.Unmarshal(in.Input, &texts); e != nil {
			var s string
			_ = json.Unmarshal(in.Input, &s)
			texts = []string{s}
		}
		data := []any{}
		for i := range texts {
			data = append(data, map[string]any{"index": i, "embedding": []float64{1, 0, 0, 0, 0, 0, 0, 0}})
		}
		requests.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer server.Close()
	m, l := newTestManager(t)
	m.models = fixtureModels{server.URL}
	m.options.DefaultEmbeddingModelKey = "fixture"
	if e := os.Remove(l.database); e != nil {
		t.Fatal(e)
	}
	for p, body := range map[string]string{"allowed/a.md": "alpha orchard", "allowed-old/b.md": "beta building"} {
		p = filepath.Join(l.spec.WorkspaceRoot, p)
		if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(p, []byte(body), 0600); e != nil {
			t.Fatal(e)
		}
	}
	cfg, e := m.config(l, true)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, e = m.runner.Run(ctx, l.database, cfg, "collection", "add", l.spec.WorkspaceRoot, "--name", "workspace"); e != nil {
		t.Fatal(e)
	}
	fixtureConfig := filepath.Join(t.TempDir(), "config.yml")
	if e = os.WriteFile(fixtureConfig, cfg, 0600); e != nil {
		t.Fatal(e)
	}
	cmd := exec.CommandContext(ctx, filepath.Join(bin, "kbx"), "--kb", l.database, "--config", fixtureConfig, "embed", "-c", "workspace")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture embed: %v: %s; requests=%d", err, output, requests.Load())
	}
	r, e := m.Search(ctx, "docs", "unseenlexicaltoken", kbase.SearchOptions{PathPrefix: "allowed", Type: "MD"})
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Results) != 1 || r.Results[0].Path != "allowed/a.md" {
		t.Fatalf("vector prefilter: %+v", r)
	}
	if !strings.Contains(strings.Join(r.RetrievalChannels, ","), "vector") {
		t.Fatalf("vector path not used: %+v", r)
	}
	if requests.Load() < 2 {
		t.Fatal("build/query did not contact fixture provider")
	}
	source := m.agents.(testSource)
	other := source["docs"]
	other.Key = "other"
	other.WorkspaceRoot = t.TempDir()
	source["other"] = other
	otherLibrary, e := m.resolve("other")
	if e != nil {
		t.Fatal(e)
	}
	if otherLibrary.database == l.database {
		t.Fatal("different libraries share storage")
	}
	if _, e = m.Search(ctx, "other", "alpha", kbase.SearchOptions{}); kbase.KindOf(e) != kbase.ErrorUnavailable {
		t.Fatalf("missing library borrowed another index: %v", e)
	}
	t.Log("ACCEPTANCE vector: model credentials/endpoint mapping, vector-only recall, prefix prefilter, and isolated library storage passed")
}
