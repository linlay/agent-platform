package memoryworker

import (
	"agent-platform/internal/models"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemxIntegration(t *testing.T) {
	binary := os.Getenv("MEMX_TEST_BINARY")
	if binary == "" {
		t.Skip("set MEMX_TEST_BINARY for subprocess integration")
	}
	inherited := filepath.Join(t.TempDir(), "other-instance")
	t.Setenv("MEMX_CONFIG_DIR", inherited)
	var calls atomic.Int32
	var token atomic.Value
	token.Store("first-key")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+token.Load().(string) {
			t.Error("wrong configured key")
		}
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		var evidence struct {
			Evidence Batch `json:"evidence"`
		}
		if len(req.Messages) != 2 || json.Unmarshal([]byte(req.Messages[1].Content), &evidence) != nil || len(evidence.Evidence.Sources) == 0 {
			t.Error("missing evidence")
			w.WriteHeader(400)
			return
		}
		source := evidence.Evidence.Sources[0]
		result, _ := json.Marshal(map[string]any{"facts": []any{map[string]any{"key": "language", "text": source.Content, "sourceId": source.ID, "quote": source.Content, "durable": true}}})
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": string(result)}}}})
	}))
	defer upstream.Close()
	registryDir := t.TempDir()
	for _, d := range []string{"providers", "models"} {
		os.Mkdir(filepath.Join(registryDir, d), 0700)
	}
	providerFile := filepath.Join(registryDir, "providers", "mock.yml")
	writeProvider := func() {
		t.Helper()
		body := fmt.Sprintf("key: mock\nbaseUrl: %s\napiKey: %s\ndefaultModel: test\n", upstream.URL, token.Load())
		if e := os.WriteFile(providerFile, []byte(body), 0600); e != nil {
			t.Fatal(e)
		}
	}
	writeProvider()
	os.WriteFile(filepath.Join(registryDir, "models", "test.yml"), []byte("key: test\nprovider: mock\nmodelId: actual\nprotocol: OPENAI\n"), 0600)
	registry, e := models.LoadModelRegistry(registryDir)
	if e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	cli := Client{Binary: binary, Root: root, ConfigDir: filepath.Join(t.TempDir(), "memx"), Timezone: "UTC"}
	syncer := &ModelConfigSync{Models: registry, Client: cli, TimeoutSeconds: 10}
	w, _, now := workerFixture(t, cli)
	w.syncConfig = syncer
	if n, e := w.run(context.Background(), now); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	path := filepath.Join(cli.ConfigDir, "models.yml")
	before, _ := os.Stat(path)
	if e := syncer.Sync(context.Background()); e != nil {
		t.Fatal(e)
	}
	after, _ := os.Stat(path)
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("unchanged config rewritten")
	}
	restarted := New(w.cfg, w.stateDir, w.chats, cli, &ModelConfigSync{Models: registry, Client: cli, TimeoutSeconds: 10}, w.eligible)
	if n, e := restarted.run(context.Background(), now.Add(24*time.Hour)); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	if calls.Load() != 1 {
		t.Fatal("completed evidence reprocessed")
	}
	for _, p := range []string{"daily/2026-10-03.md", "summary.md"} {
		b, e := os.ReadFile(filepath.Join(root, p))
		if e != nil || !strings.Contains(string(b), "请使用中文") {
			t.Fatal(p, e)
		}
	}
	token.Store("rotated-key")
	writeProvider()
	if e := registry.ReloadProviders(); e != nil {
		t.Fatal(e)
	}
	if e := syncer.Sync(context.Background()); e != nil {
		t.Fatal(e)
	}
	batch := makeBatches(w.chats.(fakeChats).runs[0], "project", w.chats.(fakeChats).messages)[0]
	batch.BatchID = "second-batch"
	if e := cli.Call(context.Background(), "update", map[string]any{"batch": batch}, nil); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 2 {
		t.Fatal("new config unused")
	}
	if _, err := os.Stat(inherited); !os.IsNotExist(err) {
		t.Fatal("inherited config directory was used", err)
	}
}

func TestRangeMemxIntegration(t *testing.T) {
	binary := os.Getenv("MEMX_TEST_BINARY")
	if binary == "" {
		t.Skip("set MEMX_TEST_BINARY for subprocess integration")
	}
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Model != "configured-extraction-model" || len(req.Messages) != 2 {
			t.Error("configuration not used")
			w.WriteHeader(400)
			return
		}
		var payload struct {
			Evidence Batch `json:"evidence"`
		}
		if json.Unmarshal([]byte(req.Messages[1].Content), &payload) != nil || len(payload.Evidence.Sources) == 0 {
			w.WriteHeader(400)
			return
		}
		src := payload.Evidence.Sources[0]
		facts, _ := json.Marshal(map[string]any{"facts": []any{map[string]any{"key": "language", "text": src.Content, "sourceId": src.ID, "quote": src.Content, "durable": true}}})
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": string(facts)}}}})
	}))
	defer upstream.Close()
	cli := Client{Binary: binary, Root: t.TempDir(), ConfigDir: t.TempDir(), Timezone: "UTC"}
	config, _ := json.Marshal(map[string]any{"schemaVersion": 1, "models": map[string]any{"extraction": map[string]any{"protocol": "openai_chat", "url": upstream.URL, "model": "configured-extraction-model", "auth": map[string]string{"type": "none"}, "timeoutSeconds": 10, "parameters": map[string]int{"maxOutputTokens": 4096}}}})
	if err := cli.SetConfig(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	worker, _, _ := workerFixture(t, cli)
	job := manualJob(worker, "2026-10-03", "2026-10-03")
	if err := worker.runRange(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if job.NewFacts == nil || *job.NewFacts != 1 || job.ProcessedBatches != 1 {
		t.Fatalf("%+v", job)
	}
	for _, path := range []string{"daily/2026-10-03.md", "summary.md"} {
		b, err := os.ReadFile(filepath.Join(cli.Root, path))
		if err != nil || !strings.Contains(string(b), "请使用中文") {
			t.Fatal(path, err, string(b))
		}
	}
	job = manualJob(worker, "2026-10-02", "2026-10-04")
	if err := worker.runRange(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || job.ReusedBatches != 1 {
		t.Fatal("overlapping range re-extracted", calls.Load(), job)
	}
	if _, err := os.Stat(filepath.Join(worker.stateDir, "checkpoint.json")); !os.IsNotExist(err) {
		t.Fatal("range touched incremental state", err)
	}
}
