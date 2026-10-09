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
	"sync"
	"time"

	"agent-platform/internal/builtins"
	"agent-platform/internal/knowledge"
	"agent-platform/internal/models"
)

// Options contains Platform-owned configuration. KBX owns all index operations.
type Options struct {
	RuntimeDir, DefaultEmbeddingModelKey, EmbeddingPrompt string
	StateDir                                              string
	Debounce, ReconcileInterval, MaintenanceTimeout       time.Duration
	MaxParallel                                           int
	ConfigSource                                          *ModelConfigSource
}
type embeddingModels interface {
	GetEmbedding(string) (models.ModelDefinition, models.ProviderDefinition, error)
}

type Manager struct {
	options    Options
	agents     knowledge.AgentSource
	models     embeddingModels
	runner     Runner
	mu         sync.Mutex
	ctx        context.Context
	cancel     context.CancelFunc
	workers    map[string]*libraryWorker
	gate       chan struct{}
	wg         sync.WaitGroup
	closed     bool
	startError error
	lockFile   *os.File
}
type library struct {
	spec     knowledge.AgentSpec
	database string
}

func NewManager(options Options, agents knowledge.AgentSource, registry *models.ModelRegistry) *Manager {
	if options.StateDir == "" {
		options.StateDir = filepath.Join(options.RuntimeDir, ".state")
	}
	if options.Debounce <= 0 {
		options.Debounce = 500 * time.Millisecond
	}
	if options.ReconcileInterval <= 0 {
		options.ReconcileInterval = 5 * time.Minute
	}
	if options.MaintenanceTimeout <= 0 {
		options.MaintenanceTimeout = 35 * time.Minute
	}
	if options.MaxParallel <= 0 {
		options.MaxParallel = 2
	}
	m := &Manager{options: options, agents: agents, runner: cliRunner{}}
	m.workers = make(map[string]*libraryWorker)
	m.gate = make(chan struct{}, options.MaxParallel)
	if registry != nil {
		m.models = registry
	}
	return m
}
func unavailable(message string) error {
	return &knowledge.PolicyError{Kind: knowledge.ErrorUnavailable, Message: message}
}
func (m *Manager) resolve(key string) (library, error) {
	if m.agents == nil {
		return library{}, unavailable("KBX capability source is unavailable")
	}
	spec, ok := m.agents.Agent(strings.TrimSpace(key))
	if !ok || !spec.Config.Enabled {
		return library{}, &knowledge.PolicyError{Kind: knowledge.ErrorNotFound, Message: "knowledge base not found for agent"}
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
	if spec.Config.Chunk.Unit != knowledge.ChunkUnitChars && spec.Config.Chunk.MaxTokens != 0 && (spec.Config.Chunk.MaxTokens != 1000 || spec.Config.Chunk.OverlapTokens != 100) {
		return library{}, fmt.Errorf("KBX custom chunk sizes require unit: chars")
	}
	defaults := knowledge.DefaultConfig().Retrieval
	r := spec.Config.Retrieval
	if r.RRFK != 0 && (r.RRFK != defaults.RRFK || r.VectorWeight != defaults.VectorWeight || r.FTSWeight != defaults.FTSWeight) {
		return library{}, fmt.Errorf("KBX owns retrieval ranking; custom RRF weights are unsupported")
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
		Chunk            knowledge.ChunkConfig
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
func (m *Manager) ValidateStartupStorage() map[string]error {
	failures := map[string]error{}
	for _, a := range m.agents.Agents() {
		if _, e := m.resolve(a.Key); e != nil {
			failures[a.Key] = e
		}
	}
	return failures
}

func (m *Manager) RuntimeSnapshot() knowledge.RuntimeState {
	_, err := builtins.ResolveProcessBuiltin("kbx")
	s := knowledge.RuntimeState{Engine: "kbx", Available: err == nil}
	if err != nil {
		s.LastError = "managed KBX executable unavailable"
	}
	return s
}
func (m *Manager) ProbeRuntime(ctx context.Context) (bool, knowledge.RuntimeState, error) {
	required := false
	if len(m.agents.Agents()) == 0 {
		return false, knowledge.RuntimeState{Engine: "kbx"}, nil
	}
	for _, a := range m.agents.Agents() {
		required = required || a.Requirement == knowledge.RequirementRequired
	}
	s := m.RuntimeSnapshot()
	m.mu.Lock()
	startErr := m.startError
	m.mu.Unlock()
	if startErr != nil {
		s.Available = false
		s.LastError = startErr.Error()
		return required, s, unavailable(s.LastError)
	}
	if !s.Available {
		return required, s, unavailable(s.LastError)
	}
	// Probe the maintenance contract without opening an index or accessing models.
	err := m.probeMaintenance(ctx)
	if err != nil {
		s.Available = false
		s.LastError = err.Error()
	}
	return required, s, err
}
func (m *Manager) config(l library, embedding bool) ([]byte, error) {
	cfg := map[string]any{"models": map[string]any{"embedding": nil, "query_expansion": nil, "reranker": nil, "graph_extraction": nil}}
	chunk := l.spec.Config.Chunk
	maxChars, overlap := 3600, 540
	if chunk.Unit == knowledge.ChunkUnitChars {
		maxChars, overlap = chunk.MaxChars, chunk.OverlapChars
	} else if chunk.MaxTokens != 0 && (chunk.MaxTokens != 1000 || chunk.OverlapTokens != 100) {
		return nil, fmt.Errorf("KBX requires character chunking for custom sizes; configure unit: chars")
	}
	cfg["chunking"] = map[string]any{"strategy": "window", "max_chars": maxChars, "overlap_chars": overlap}
	key := m.options.DefaultEmbeddingModelKey
	if embedding && m.options.ConfigSource != nil {
		raw, err := m.options.ConfigSource.Snapshot()
		if err != nil {
			return nil, err
		}
		var shared map[string]any
		if err = json.Unmarshal(raw, &shared); err != nil {
			return nil, err
		}
		cfg["models"] = shared["models"]
	} else if embedding && key != "" {
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
		if m.options.EmbeddingPrompt != "" {
			role["prompt"] = m.options.EmbeddingPrompt
		}
		if model.Embedding.BatchSize > 0 {
			role["batch_size"] = model.Embedding.BatchSize
		}
		if provider.APIKey != "" {
			role["api_key"] = provider.APIKey
		}
		cfg["models"].(map[string]any)["embedding"] = role
	}
	return json.Marshal(cfg)
}
func (m *Manager) call(ctx context.Context, l library, embedding bool, out any, args ...string) error {
	state := knowledge.Status{}
	if m.workerStatus(l, &state) && (state.Indexes == nil || !state.Indexes.FTS.Ready) {
		return &readinessError{PolicyError: &knowledge.PolicyError{Kind: knowledge.ErrorUnavailable, Message: "KBX index is not ready; wait for refreshId and inspect kbase_status"}, state: state}
	}
	if _, err := os.Stat(l.database); err != nil {
		m.mu.Lock()
		w := m.workers[l.spec.Key]
		m.mu.Unlock()
		if w != nil && w.library.database == l.database {
			w.changed("", true)
		}
		return unavailable("KBX index is not ready; use kbase_status and kbase_refresh to schedule and wait for indexing")
	}
	cfg, err := m.config(l, embedding)
	if err != nil {
		return err
	}
	data, err := m.runner.Run(ctx, l.database, cfg, args...)
	if failure := readerFailure(data, args[0]); failure != nil {
		return failure
	}
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
