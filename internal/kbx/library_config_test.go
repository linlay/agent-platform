package kbx

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/kbasescenter"
	"agent-platform/internal/models"
)

func libraryModelFixture(t *testing.T, url string) (*ModelConfigSource, string) {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"models", "providers"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	provider := filepath.Join(root, "providers", "fixture.yml")
	if err := os.WriteFile(provider, []byte("key: fixture\nbaseUrl: "+url+"\napiKey: fixture-key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(root, "models", key+".yml"), []byte("key: "+key+"\nprovider: fixture\ntype: embedding\nmodelId: model-"+key+"\nembedding:\n  dimension: 8\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	registry, err := models.LoadModelRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	return &ModelConfigSource{File: filepath.Join(root, "state", "kbx", "index.yml"), Registry: registry, ModelKey: "a", Prompt: "raw"}, provider
}
func TestLibraryModelIsolationAndContract(t *testing.T) {
	source, provider := libraryModelFixture(t, "https://example.test")
	e := NewCenterEngineWithSource(source)
	a := kbasescenter.Definition{}
	b := kbasescenter.Definition{Models: &kbasescenter.ModelsConfig{Embedding: &kbasescenter.EmbeddingConfig{ModelKey: "b", Prompt: "qwen3"}}}
	fa, fb := e.VectorFingerprint(a), e.VectorFingerprint(b)
	if fa == fb || fa == "" {
		t.Fatal("model contracts collapsed")
	}
	for _, tc := range []struct {
		d             kbasescenter.Definition
		model, prompt string
	}{{a, "model-a", "raw"}, {b, "model-b", "qwen3"}} {
		raw, err := e.libraryConfig(tc.d, true)
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Models struct {
				Embedding struct{ Model, Prompt string }
			}
		}
		if err = json.Unmarshal(raw, &cfg); err != nil {
			t.Fatal(err)
		}
		if cfg.Models.Embedding.Model != tc.model || cfg.Models.Embedding.Prompt != tc.prompt {
			t.Fatalf("wrong library config: %+v", cfg)
		}
	}
	if err := os.WriteFile(provider, []byte("key: fixture\nbaseUrl: https://example.test\napiKey: rotated\nembedding:\n  timeout: 20\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := source.Registry.ReloadProviders(); err != nil {
		t.Fatal(err)
	}
	if fa != e.VectorFingerprint(a) || fb != e.VectorFingerprint(b) {
		t.Fatal("credential/transport tuning invalidates vectors")
	}
	raw, err := e.libraryConfig(b, true)
	if err != nil || !strings.Contains(string(raw), "rotated") {
		t.Fatal("credentials not refreshed", err)
	}
	b.Models.Embedding.ModelKey = "missing"
	if _, err := e.libraryConfig(b, true); err == nil {
		t.Fatal("missing model silently accepted")
	}
	if _, err := e.libraryConfig(b, false); err != nil {
		t.Fatal("text read depends on model", err)
	}
}

func TestVectorRebuildResumesWithoutForcingAgain(t *testing.T) {
	source, _ := libraryModelFixture(t, "https://example.test")
	e := NewCenterEngineWithSource(source)
	calls := []string{}
	e.runner = runFunc(func(_ context.Context, _ string, _ []byte, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		op := args[0]
		if op != "status" && op != "embed" {
			t.Fatalf("vector rebuild scanned sources: %v", args)
		}
		if len(calls) == 2 {
			return []byte(`{"schemaVersion":1,"type":"kbx.maintenance.response","operation":"embed","status":"partial","exitCode":1,"stopReason":"SESSION_LIMIT","continuation":{"canRetry":true}}`), fmt.Errorf("partial")
		}
		return []byte(fmt.Sprintf(`{"schemaVersion":1,"type":"kbx.maintenance.response","operation":%q,"status":"complete","exitCode":0,"index":{"selected":{"documents":1,"fullText":{"ready":true},"vector":{"complete":true,"configuredContractCompatible":true}}}}`, op)), nil
	})
	if err := e.RebuildVectors(context.Background(), "unused", kbasescenter.Definition{}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || !strings.Contains(calls[1], "--force") || strings.Contains(calls[2], "--force") {
		t.Fatalf("unsafe resume: %v", calls)
	}
}

func TestPendingVectorsQueryUsesTextAndStrictVectorFails(t *testing.T) {
	source, _ := libraryModelFixture(t, "https://example.test")
	e := NewCenterEngineWithSource(source)
	e.runner = runFunc(func(_ context.Context, _ string, cfg []byte, args ...string) ([]byte, error) {
		if args[0] != "query" || !strings.Contains(string(cfg), `"embedding":null`) {
			t.Fatalf("pending vectors used: %s %v", cfg, args)
		}
		return responseJSON(map[string]any{"results": []any{}}), nil
	})
	d := kbasescenter.Definition{VectorsPending: true}
	if _, err := e.ReadLibrary(context.Background(), "unused", d, "query", "needle", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ReadLibrary(context.Background(), "unused", d, "vsearch", "needle", 10); err == nil {
		t.Fatal("strict vector read accepted while pending")
	}
}

func TestSourceRefreshAlsoForcesChangedPlatformVectorContract(t *testing.T) {
	source, _ := libraryModelFixture(t, "https://example.test")
	e := NewCenterEngineWithSource(source)
	fake := &maintenanceFake{}
	forced := false
	e.runner = runFunc(func(ctx context.Context, db string, cfg []byte, args ...string) ([]byte, error) {
		if args[0] == "embed" {
			forced = strings.Contains(strings.Join(args, " "), "--force")
			return []byte(`{"schemaVersion":1,"type":"kbx.maintenance.response","operation":"embed","status":"complete","exitCode":0,"index":{"selected":{"documents":1,"vector":{"complete":true,"configuredContractCompatible":true}}}}`), nil
		}
		return fake.Run(ctx, db, cfg, args...)
	})
	root, _ := filepath.EvalSymlinks(t.TempDir())
	d := kbasescenter.Definition{VectorsPending: true, Collections: []kbasescenter.Collection{{Name: "docs", SourcePath: root}}}
	if err := e.UpdateLibrary(context.Background(), filepath.Join(t.TempDir(), "index.sqlite"), d, nil); err != nil {
		t.Fatal(err)
	}
	if !forced {
		t.Fatal("full source refresh failed to rebuild changed endpoint/model contract")
	}
}
