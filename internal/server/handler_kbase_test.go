package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/agent/kbase"
	"agent-platform/internal/models"
)

type handlerKBaseAgentSource map[string]kbase.AgentSpec

func (s handlerKBaseAgentSource) Agents() []kbase.AgentSpec {
	out := make([]kbase.AgentSpec, 0, len(s))
	for _, spec := range s {
		out = append(out, spec)
	}
	return out
}

func (s handlerKBaseAgentSource) Agent(key string) (kbase.AgentSpec, bool) {
	spec, ok := s[key]
	return spec, ok
}

func TestHandleKBaseStatusMappingAndMethods(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	manager := kbase.NewManager(kbase.ManagerOptions{RuntimeDir: filepath.Join(root, "runtime")}, handlerKBaseAgentSource{
		"docs":  handlerKBaseAgent("docs", kbase.Mode, workspace),
		"react": handlerKBaseAgent("react", "REACT", workspace),
	}, handlerKBaseModelRegistry(t, root))
	srv := &Server{deps: Dependencies{KBase: manager}}

	tests := []struct {
		name   string
		method string
		path   string
		body   string
		want   int
		allow  string
	}{
		{name: "status", method: http.MethodGet, path: "/api/kbase/docs/status", want: http.StatusOK},
		{name: "refresh", method: http.MethodPost, path: "/api/kbase/docs/refresh", body: `{}`, want: http.StatusOK},
		{name: "unknown agent", method: http.MethodGet, path: "/api/kbase/missing/status", want: http.StatusNotFound},
		{name: "wrong mode", method: http.MethodGet, path: "/api/kbase/react/status", want: http.StatusForbidden},
		{name: "unknown agent precedes method", method: http.MethodPost, path: "/api/kbase/missing/status", want: http.StatusNotFound},
		{name: "wrong mode precedes method", method: http.MethodPost, path: "/api/kbase/react/status", want: http.StatusForbidden},
		{name: "bad path", method: http.MethodGet, path: "/api/kbase/docs", want: http.StatusNotFound},
		{name: "status method", method: http.MethodPost, path: "/api/kbase/docs/status", want: http.StatusMethodNotAllowed, allow: http.MethodGet},
		{name: "refresh method", method: http.MethodGet, path: "/api/kbase/docs/refresh", want: http.StatusMethodNotAllowed, allow: http.MethodPost},
		{name: "invalid body", method: http.MethodPost, path: "/api/kbase/docs/refresh", body: `{`, want: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			if tt.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rr := httptest.NewRecorder()
			srv.handleKBase(rr, req)
			if rr.Code != tt.want {
				t.Fatalf("status=%d want=%d body=%s", rr.Code, tt.want, rr.Body.String())
			}
			if tt.allow != "" && rr.Header().Get("Allow") != tt.allow {
				t.Fatalf("Allow=%q want=%q", rr.Header().Get("Allow"), tt.allow)
			}
		})
	}

	nilServer := &Server{}
	rr := httptest.NewRecorder()
	nilServer.handleKBase(rr, httptest.NewRequest(http.MethodGet, "/api/kbase/docs/status", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil manager status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func handlerKBaseAgent(key string, mode string, workspace string) kbase.AgentSpec {
	return kbase.AgentSpec{
		Key: key, Mode: mode, WorkspaceRoot: workspace,
		Config: kbase.AgentConfig{
			Embedding: kbase.EmbeddingConfig{ModelKey: "embedding"},
			Storage:   kbase.StorageConfig{Location: "runtime"},
			Include:   []string{"**/*.md"},
			Exclude:   kbase.DefaultExcludePatterns(),
			Chunk:     kbase.DefaultChunkConfig(),
			Retrieval: kbase.RetrievalConfig{TopK: 5, VectorWeight: 0.7, FTSWeight: 0.3},
		},
	}
}

func handlerKBaseModelRegistry(t *testing.T, root string) *models.ModelRegistry {
	t.Helper()
	registries := filepath.Join(root, "registries")
	providers := filepath.Join(registries, "providers")
	modelDir := filepath.Join(registries, "models")
	if err := os.MkdirAll(providers, 0o755); err != nil {
		t.Fatalf("mkdir providers: %v", err)
	}
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatalf("mkdir models: %v", err)
	}
	provider := strings.Join([]string{
		"key: mock", "baseUrl: http://127.0.0.1:1", "apiKey: test",
		"embedding:", "  model: embedding", "  dimension: 3", "  timeout: 1",
	}, "\n")
	model := strings.Join([]string{
		"key: embedding", "provider: mock", "type: embedding", "modelId: embedding",
		"embedding:", "  dimension: 3", "  timeout: 1",
	}, "\n")
	if err := os.WriteFile(filepath.Join(providers, "mock.yml"), []byte(provider), 0o644); err != nil {
		t.Fatalf("write provider: %v", err)
	}
	if err := os.WriteFile(filepath.Join(modelDir, "embedding.yml"), []byte(model), 0o644); err != nil {
		t.Fatalf("write model: %v", err)
	}
	registry, err := models.LoadModelRegistry(registries)
	if err != nil {
		t.Fatalf("load model registry: %v", err)
	}
	return registry
}
