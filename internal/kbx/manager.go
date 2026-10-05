package kbx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-platform/internal/builtins"
	"agent-platform/internal/kbase"
	"agent-platform/internal/models"
)

// Options contains Platform-owned configuration. KBX owns all index operations.
type Options struct{ RuntimeDir, DefaultEmbeddingModelKey string }
type embeddingModels interface {
	GetEmbedding(string) (models.ModelDefinition, models.ProviderDefinition, error)
}

type Manager struct {
	options Options
	agents  kbase.AgentSource
	models  embeddingModels
	runner  Runner
}
type library struct {
	spec     kbase.AgentSpec
	database string
}

const updateUnavailable = "KBX singleton update/reconnect protocol is not available in the installed integration; index maintenance is unavailable"

func NewManager(options Options, agents kbase.AgentSource, registry *models.ModelRegistry) *Manager {
	m := &Manager{options: options, agents: agents, runner: cliRunner{}}
	if registry != nil {
		m.models = registry
	}
	return m
}
func unavailable(message string) error {
	return &kbase.PolicyError{Kind: kbase.ErrorUnavailable, Message: message}
}
func (m *Manager) resolve(key string) (library, error) {
	if m.agents == nil {
		return library{}, unavailable("KBX capability source is unavailable")
	}
	spec, ok := m.agents.Agent(strings.TrimSpace(key))
	if !ok || !spec.Config.Enabled {
		return library{}, &kbase.PolicyError{Kind: kbase.ErrorNotFound, Message: "knowledge base not found for agent"}
	}
	if spec.Key == "" || filepath.Base(spec.Key) != spec.Key || spec.Key == "." || spec.Key == ".." {
		return library{}, fmt.Errorf("invalid knowledge-base agent key")
	}
	if strings.TrimSpace(spec.WorkspaceRoot) == "" {
		return library{}, fmt.Errorf("KBX requires workspaceRoot")
	}
	root, err := filepath.Abs(spec.WorkspaceRoot)
	if err != nil {
		return library{}, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return library{}, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return library{}, err
	}
	if !info.IsDir() {
		return library{}, fmt.Errorf("KBX workspace must be a directory")
	}
	spec.WorkspaceRoot = root
	if spec.Config.Chunk.Unit != kbase.ChunkUnitChars && spec.Config.Chunk.MaxTokens != 0 && (spec.Config.Chunk.MaxTokens != 1000 || spec.Config.Chunk.OverlapTokens != 100) {
		return library{}, fmt.Errorf("KBX custom chunk sizes require unit: chars")
	}
	defaults := kbase.DefaultConfig().Retrieval
	r := spec.Config.Retrieval
	if r.RRFK != 0 && (r.RRFK != defaults.RRFK || r.VectorWeight != defaults.VectorWeight || r.FTSWeight != defaults.FTSWeight) {
		return library{}, fmt.Errorf("KBX owns retrieval ranking; custom legacy RRF weights are unsupported")
	}
	for _, pattern := range append(append([]string{}, spec.Config.Include...), spec.Config.Exclude...) {
		if strings.ContainsAny(pattern, "[]{}\\") {
			return library{}, fmt.Errorf("KBX capability globs support only *, ? and **")
		}
		for _, segment := range strings.Split(pattern, "/") {
			if segment == ".." || (strings.Contains(segment, "**") && segment != "**") {
				return library{}, fmt.Errorf("invalid KBX capability glob")
			}
		}
	}

	// A changed workspace or indexing policy must never reuse another scope's index.
	identity, _ := json.Marshal(struct {
		Root             string
		Include, Exclude []string
		Chunk            kbase.ChunkConfig
	}{root, spec.Config.Include, spec.Config.Exclude, spec.Config.Chunk})
	sum := sha256.Sum256(identity)
	storageRoot, err := canonicalRoot(m.options.RuntimeDir)
	if err != nil {
		return library{}, err
	}
	base := filepath.Join(storageRoot, spec.Key, "kbx")
	if spec.Config.Storage.Location == "workspace" {
		storageRoot = root
		base = filepath.Join(root, ".kbx-platform", spec.Key)
	}
	database, err := filepath.Abs(filepath.Join(base, hex.EncodeToString(sum[:8]), "index.sqlite"))
	if err != nil {
		return library{}, err
	}
	// Do not follow a substituted package to another knowledge base.
	for p := filepath.Dir(database); p != storageRoot && p != filepath.Dir(p); p = filepath.Dir(p) {
		if st, e := os.Lstat(p); e == nil && st.Mode()&os.ModeSymlink != 0 {
			return library{}, fmt.Errorf("KBX storage must not contain symlinks")
		}
	}
	if st, e := os.Lstat(database); e == nil && st.Mode()&os.ModeSymlink != 0 {
		return library{}, fmt.Errorf("KBX database must not be a symlink")
	}
	return library{spec, database}, nil
}
func (m *Manager) ValidateAgent(key string) error { _, err := m.resolve(key); return err }
func (m *Manager) ValidateConfiguration() error {
	seen := map[string]string{}
	for _, a := range m.agents.Agents() {
		l, e := m.resolve(a.Key)
		if e != nil {
			continue
		}
		if prev := seen[l.database]; prev != "" && prev != a.Key {
			return fmt.Errorf("KBX storage shared by %s and %s", prev, a.Key)
		}
		seen[l.database] = a.Key
	}
	return nil
}
func (m *Manager) ValidateAndAdoptStartupStorageContracts() map[string]error {
	failures := map[string]error{}
	for _, a := range m.agents.Agents() {
		if _, e := m.resolve(a.Key); e != nil {
			failures[a.Key] = e
		}
	}
	return failures
}

// Current KBX has no reconnectable update command. These hooks deliberately do
// not start the legacy watcher or infer completion from the old one-shot update.
func (m *Manager) Start(context.Context)             {}
func (m *Manager) ReconcileWatchers(context.Context) {}
func (m *Manager) Close(context.Context) error       { return nil }
func (m *Manager) Refresh(_ context.Context, key string, _ kbase.RefreshOptions) (kbase.RefreshResult, error) {
	if err := m.ValidateAgent(key); err != nil {
		return kbase.RefreshResult{}, err
	}
	return kbase.RefreshResult{}, unavailable(updateUnavailable)
}
func (m *Manager) RefreshOperationStatus(key, _ string) (string, error) {
	if err := m.ValidateAgent(key); err != nil {
		return "", err
	}
	return "", unavailable(updateUnavailable)
}
func (m *Manager) RuntimeSnapshot() kbase.LanceEngineState {
	_, err := builtins.ResolveProcessBuiltin("kbx")
	s := kbase.LanceEngineState{Engine: "kbx", Available: err == nil}
	if err != nil {
		s.LastError = "managed KBX executable unavailable"
	}
	return s
}
func (m *Manager) ProbeSidecar(ctx context.Context) (bool, kbase.LanceEngineState, error) {
	required := false
	if len(m.agents.Agents()) == 0 {
		return false, kbase.LanceEngineState{Engine: "kbx"}, nil
	}
	for _, a := range m.agents.Agents() {
		required = required || a.Requirement == kbase.RequirementRequired
	}
	s := m.RuntimeSnapshot()
	if !s.Available {
		return required, s, unavailable(s.LastError)
	}
	// Validate reader flags, not merely a binary filename/version.
	_, err := m.runner.Run(ctx, "unused", []byte("{}"), "search", "--filter-help")
	if err != nil {
		s.Available = false
		s.LastError = "KBX chunk/filter CLI is unavailable"
	}
	if err == nil {
		s.LastError = updateUnavailable
		err = unavailable(updateUnavailable)
	}
	return required, s, err
}
func (m *Manager) config(l library, embedding bool) ([]byte, error) {
	cfg := map[string]any{"models": map[string]any{"embedding": nil, "query_expansion": nil, "reranker": nil, "graph_extraction": nil}}
	chunk := l.spec.Config.Chunk
	maxChars, overlap := 3600, 540
	if chunk.Unit == kbase.ChunkUnitChars {
		maxChars, overlap = chunk.MaxChars, chunk.OverlapChars
	} else if chunk.MaxTokens != 0 && (chunk.MaxTokens != 1000 || chunk.OverlapTokens != 100) {
		return nil, fmt.Errorf("KBX requires character chunking for custom sizes; configure unit: chars")
	}
	cfg["chunking"] = map[string]any{"strategy": "window", "max_chars": maxChars, "overlap_chars": overlap}
	key := l.spec.Config.Embedding.ModelKey
	if key == "" {
		key = m.options.DefaultEmbeddingModelKey
	}
	if embedding && key != "" {
		if m.models == nil {
			return nil, unavailable("KBX embedding registry unavailable")
		}
		model, provider, err := m.models.GetEmbedding(key)
		if err != nil {
			return nil, err
		}
		endpoint := model.Embedding.EndpointPath
		if endpoint == "" {
			endpoint = "/v1/embeddings"
		}
		if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
			endpoint = strings.TrimRight(provider.BaseURL, "/") + "/" + strings.TrimLeft(endpoint, "/")
		}
		timeout := model.Embedding.Timeout
		if timeout <= 0 {
			timeout = provider.Embedding.Timeout
		}
		if timeout <= 0 {
			timeout = 60
		}
		role := map[string]any{"url": endpoint, "model": model.ModelID, "timeout_ms": timeout * 1000, "prompt": "raw"}
		if provider.APIKey != "" {
			role["api_key"] = provider.APIKey
		}
		cfg["models"].(map[string]any)["embedding"] = role
	}
	return json.Marshal(cfg)
}
func (m *Manager) call(ctx context.Context, l library, embedding bool, out any, args ...string) error {
	if _, err := os.Stat(l.database); err != nil {
		return unavailable("KBX index is not ready; " + updateUnavailable)
	}
	cfg, err := m.config(l, embedding)
	if err != nil {
		return err
	}
	data, err := m.runner.Run(ctx, l.database, cfg, args...)
	if err != nil {
		return unavailable(err.Error())
	}
	if err = decodeEnvelope(data, out); err != nil {
		return unavailable(err.Error())
	}
	return nil
}
func readerContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// Resolve trusted storage-root aliases such as macOS /var and /tmp, while
// retaining non-existent descendants. Per-library symlinks remain forbidden.
func canonicalRoot(p string) (string, error) {
	p, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	missing := []string{}
	for {
		if _, err = os.Lstat(p); err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		missing = append(missing, filepath.Base(p))
		p = filepath.Dir(p)
	}
	p, err = filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	for i := len(missing) - 1; i >= 0; i-- {
		p = filepath.Join(p, missing[i])
	}
	return p, nil
}
