package kbx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/kbases"
	"agent-platform/internal/knowledge"
	"agent-platform/internal/models"
)

// Explicit opt-in: only synthetic documents are sent to the selected deployment
// models. Credentials and endpoints are never logged; indexes/config snapshots
// live under t.TempDir. No deployment configuration or source library is changed.
func TestLiveRetrievalDeployment(t *testing.T) {
	registryRoot := os.Getenv("KBX_LIVE_REGISTRY")
	expansionKey := os.Getenv("KBX_LIVE_EXPANSION_MODEL")
	if registryRoot == "" || expansionKey == "" {
		t.Skip("set KBX_LIVE_REGISTRY and KBX_LIVE_EXPANSION_MODEL to opt in to real model calls")
	}
	setupLibraryBinary(t)
	registry, err := models.LoadModelRegistry(registryRoot)
	if err != nil {
		t.Fatal("cannot load deployment model registry")
	}
	root := t.TempDir()
	source := &ModelConfigSource{File: filepath.Join(root, "state", "kbx", "index.yml"), Registry: registry, ModelKey: os.Getenv("KBX_LIVE_EMBEDDING_MODEL")}
	t.Logf("deployment selection: expansion=%s embedding=%s", expansionKey, source.ModelKey)
	roles := &kbases.ModelsConfig{QueryExpansion: &kbases.QueryModelConfig{ModelKey: expansionKey}}
	if key := os.Getenv("KBX_LIVE_RERANKER_MODEL"); key != "" {
		roles.Reranker = &kbases.QueryModelConfig{ModelKey: key}
	} else {
		t.Log("NOT TESTED: real reranker (KBX_LIVE_RERANKER_MODEL unset)")
	}
	preflight := NewManager(Options{ConfigSource: source}, nil, nil)
	if _, err = preflight.queryConfig(defaultLibraryConfig, kbases.Definition{Models: roles}, "query", nil); err != nil {
		t.Fatal(err)
	}
	engine := NewLibraryEngineWithSource(source)
	libraryService, err := kbases.New(context.Background(), filepath.Join(root, "kbases"), filepath.Join(root, "ru-kbases"), engine)
	if err != nil {
		t.Fatal(err)
	}
	defer libraryService.Close(context.Background())
	no, top := false, 3
	collections := []kbases.Collection{}
	for _, name := range []string{"docs", "excluded"} {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		corpus := map[string]string{
			"device.md":   "# 终端遗失处置\n员工的笔记本电脑丢失或被盗，应立即通知安全团队，撤销设备证书并执行远程擦除。",
			"audit.md":    "# 审计日志保留\nQuartzOrchid 系统的审计日志保存 180 天。日志超过保留期限后自动清理。",
			"rollback.md": "# 版本发布回退\n新版本上线后出现故障，应停止发布并回滚到上一稳定版本，然后检查服务健康状态。",
			"leave.md":    "# 请假申请\n员工请假需在办公系统填写日期并提交直属主管审批。",
			"travel.md":   "# 差旅报销\n出差费用报销需要有效发票和已批准的出差申请。",
			"password.md": "# 密码重置\n忘记密码后通过统一身份认证门户重置，并确认多因素认证设置。",
		}
		if name == "excluded" {
			corpus = map[string]string{"private.md": "QuartzOrchid EXCLUDED_FIXTURE_ONLY 审计日志永久保留。"}
		}
		for file, body := range corpus {
			if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
		}
		c := kbases.Collection{Name: name, SourcePath: dir}
		if name == "excluded" {
			c.DefaultQuery = &no
		}
		collections = append(collections, c)
	}
	d, err := libraryService.Create(kbases.Input{Name: "Synthetic live retrieval acceptance", Collections: collections, Models: roles, Retrieval: &knowledge.RetrievalSettings{TopK: &top}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = libraryService.Refresh(d.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		d, err = libraryService.Get(d.ID)
		if err == nil && d.State == "ready" && !d.Indexing {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if d.State != "ready" || d.Indexing || (source.ModelKey != "" && (d.Degraded || d.VectorsPending)) {
		t.Fatal("synthetic library did not become ready")
	}
	agentConfig, _ := knowledge.ParseConfig(map[string]any{"libraryId": d.ID})
	m := NewManager(Options{ConfigSource: source, KBases: libraryService}, testSource{"live": {Key: "live", Config: agentConfig}}, nil)
	baseRunner := m.runner
	type step struct {
		Backend, Status, Reason string
		LatencyMS               int64 `json:"latencyMs"`
		ResultCount             int   `json:"resultCount"`
		CacheHit                bool  `json:"cacheHit"`
	}
	var steps []step
	m.runner = runFunc(func(ctx context.Context, db string, cfg []byte, args ...string) ([]byte, error) {
		steps = nil
		raw, err := baseRunner.Run(ctx, db, cfg, args...)
		var response struct{ Trace struct{ Steps []step } }
		if decodeEnvelope(raw, &response) == nil {
			for _, s := range response.Trace.Steps {
				if s.Backend == "query_expansion" || s.Backend == "reranker" {
					steps = append(steps, s)
				}
			}
		}
		return raw, err
	})
	run := func(t *testing.T, label, query, want string, o knowledge.SearchOptions) knowledge.SearchResult {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		start := time.Now()
		r, err := m.Search(ctx, "live", query, o)
		if err != nil {
			t.Fatal("query execution failed:", err)
		}
		paths := []string{}
		for _, h := range r.Results {
			if !strings.HasPrefix(h.Path, "docs/") {
				t.Fatal("default query leaked an excluded collection")
			}
			paths = append(paths, h.Path)
		}
		t.Logf("case=%s elapsed_ms=%d limit=%d hit_at_3=%v paths=%v optional_unavailable=%v model_steps=%+v", label, time.Since(start).Milliseconds(), r.Limit, slices.Contains(paths, want), paths, r.OptionalUnavailable, steps)
		return r
	}
	t.Run("quality_and_latency", func(t *testing.T) {
		for _, tc := range []struct{ name, query, want string }{
			{"exact", "QuartzOrchid", "docs/audit.md"},
			{"paraphrase_device", "电脑不见了该找谁处理", "docs/device.md"},
			{"paraphrase_rollback", "上线后出问题怎么恢复旧版", "docs/rollback.md"},
		} {
			run(t, tc.name+"/baseline", tc.query, tc.want, knowledge.SearchOptions{NoGraph: true, Rerank: &no, QueryExpansion: &no})
			r := run(t, tc.name+"/models", tc.query, tc.want, knowledge.SearchOptions{NoGraph: true})
			for _, role := range []string{"query_expansion", "reranker"} {
				if role == "reranker" && roles.Reranker == nil {
					continue
				}
				ok := false
				for _, s := range steps {
					if s.Backend == role && s.Status == "ok" {
						ok = true
					}
				}
				if !ok || slices.Contains(r.OptionalUnavailable, role) {
					t.Fatalf("live model role did not succeed: %s", role)
				}
			}
			if tc.name == "exact" {
				run(t, "exact/warm", tc.query, tc.want, knowledge.SearchOptions{NoGraph: true})
			}
		}
	})
	// Faults alter only this call's private config, strip credentials, and never
	// change a deployment provider or use real documents.
	for _, failure := range []string{"http_503", "timeout", "invalid_response"} {
		t.Run(failure, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" {
					t.Error("deployment credential sent to fault fixture")
				}
				switch failure {
				case "http_503":
					http.Error(w, "synthetic unavailable", http.StatusServiceUnavailable)
				case "timeout":
					select {
					case <-r.Context().Done():
					case <-time.After(time.Second):
					}
				case "invalid_response":
					_, _ = w.Write([]byte(`{"choices":[]}`))
				}
			}))
			defer server.Close()
			originalRunner := m.runner
			defer func() { m.runner = originalRunner }()
			m.runner = runFunc(func(ctx context.Context, db string, raw []byte, args ...string) ([]byte, error) {
				var cfg map[string]any
				if err := json.Unmarshal(raw, &cfg); err != nil {
					return nil, err
				}
				role := cfg["models"].(map[string]any)["query_expansion"].(map[string]any)
				role["url"], role["timeout_ms"] = server.URL, 100
				delete(role, "api_key")
				raw, err := json.Marshal(cfg)
				if err != nil {
					return nil, err
				}
				return originalRunner.Run(ctx, db, raw, args...)
			})
			r := run(t, failure, "QuartzOrchid", "docs/audit.md", knowledge.SearchOptions{NoGraph: true, Rerank: &no, Intent: failure})
			found := false
			for _, hit := range r.Results {
				found = found || hit.Path == "docs/audit.md"
			}
			if !r.Degraded || !found || !slices.Contains(r.OptionalUnavailable, "query_expansion") {
				t.Fatal("failed model did not preserve full-text result and degradation signal")
			}
		})
	}
}
