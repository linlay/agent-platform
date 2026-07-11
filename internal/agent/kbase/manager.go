package kbase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/models"
	"agent-platform/internal/supportpkg"
	runtimewatch "agent-platform/internal/watch"
)

type Manager struct {
	options ManagerOptions
	agents  AgentSource
	models  *models.ModelRegistry
	support *supportpkg.Registry

	mu               sync.Mutex
	watchReconcileMu sync.Mutex
	watchContext     context.Context
	locks            map[string]*sync.Mutex
	watchers         map[string]watcherBinding
	running          map[string]bool
	storageRunning   map[string]bool
	storageQueued    map[string]bool
}

type ManagerOptions struct {
	RuntimeDir               string
	DefaultEmbeddingModelKey string
	RefreshDebounce          time.Duration
	ReconcileInterval        time.Duration
	Extraction               ExtractionConfig
}

type AgentSpec struct {
	Key           string
	Mode          string
	WorkspaceRoot string
	Config        AgentConfig
}

type AgentSource interface {
	Agents() []AgentSpec
	Agent(key string) (AgentSpec, bool)
}

type watcherBinding struct {
	watcher   *runtimewatch.Watcher
	cancel    context.CancelFunc
	signature string
}

func NewManager(options ManagerOptions, agents AgentSource, modelRegistry *models.ModelRegistry) *Manager {
	return &Manager{
		options:        options,
		agents:         agents,
		models:         modelRegistry,
		locks:          map[string]*sync.Mutex{},
		watchers:       map[string]watcherBinding{},
		running:        map[string]bool{},
		storageRunning: map[string]bool{},
		storageQueued:  map[string]bool{},
	}
}

func (m *Manager) WithSupportPackages(registry *supportpkg.Registry) *Manager {
	if m == nil {
		return nil
	}
	m.support = registry
	return m
}

// ValidateAgent applies the KBASE ownership policy without opening storage or
// resolving model configuration. HTTP adapters use it before method dispatch
// to preserve the existing not-found/wrong-mode status precedence.
func (m *Manager) ValidateAgent(agentKey string) error {
	_, err := m.agentSpec(agentKey)
	return err
}

func (m *Manager) Start(ctx context.Context) {
	if m == nil {
		return
	}
	m.mu.Lock()
	for key, binding := range m.watchers {
		if binding.cancel != nil {
			binding.cancel()
		}
		delete(m.watchers, key)
	}
	m.watchContext = ctx
	m.mu.Unlock()
	m.ensureWatchers(ctx)
	for _, key := range m.kbaseAgentKeys() {
		agentKey := key
		go func() {
			if _, err := m.Refresh(ctx, agentKey, RefreshOptions{Mode: "startup"}); err != nil {
				log.Printf("[kbase] startup refresh failed agent=%s: %v", agentKey, err)
			}
		}()
	}
	interval := m.options.ReconcileInterval
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.ensureWatchers(ctx)
				for _, key := range m.kbaseAgentKeys() {
					agentKey := key
					go func() {
						if _, err := m.Refresh(ctx, agentKey, RefreshOptions{Mode: "reconcile"}); err != nil {
							log.Printf("[kbase] reconcile refresh failed agent=%s: %v", agentKey, err)
						}
					}()
				}
			}
		}
	}()
}

// ReconcileWatchers applies the latest AgentSource snapshot immediately after
// an agent catalog reload. Once Start has established the manager lifecycle,
// watcher contexts remain bound to that lifecycle rather than to a short-lived
// HTTP reload request.
func (m *Manager) ReconcileWatchers(ctx context.Context) {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.watchContext == nil && ctx != nil {
		m.watchContext = ctx
	}
	watchContext := m.watchContext
	m.mu.Unlock()
	if watchContext != nil {
		ctx = watchContext
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.ensureWatchers(ctx)
}

func (m *Manager) ensureWatchers(ctx context.Context) {
	m.watchReconcileMu.Lock()
	defer m.watchReconcileMu.Unlock()

	desired := map[string]AgentSpec{}
	if m != nil && m.agents != nil {
		for _, spec := range m.agents.Agents() {
			spec.Key = strings.TrimSpace(spec.Key)
			spec.WorkspaceRoot = strings.TrimSpace(spec.WorkspaceRoot)
			if spec.Key == "" || !strings.EqualFold(strings.TrimSpace(spec.Mode), Mode) || spec.WorkspaceRoot == "" {
				continue
			}
			desired[spec.Key] = spec
		}
	}

	m.mu.Lock()
	for key, binding := range m.watchers {
		spec, ok := desired[key]
		if ok && binding.signature == watcherSignature(spec) {
			delete(desired, key)
			continue
		}
		if binding.cancel != nil {
			binding.cancel()
		}
		delete(m.watchers, key)
	}
	m.mu.Unlock()

	for _, spec := range desired {
		m.startWatcher(ctx, spec)
	}
}

func (m *Manager) startWatcher(ctx context.Context, spec AgentSpec) {
	agentKey := strings.TrimSpace(spec.Key)
	workspace := strings.TrimSpace(spec.WorkspaceRoot)
	debounce := m.options.RefreshDebounce
	if debounce <= 0 {
		debounce = 2 * time.Second
	}
	watchCtx, cancel := context.WithCancel(ctx)
	matchers := compileMatchers(append(DefaultExcludePatterns(), spec.Config.Exclude...))
	watcher, err := runtimewatch.Start(watchCtx, runtimewatch.Spec{
		LogPrefix: "[kbase]",
		Roots: []runtimewatch.Root{{
			Path:      workspace,
			Label:     agentKey,
			Recursive: true,
			ShouldTraverse: func(path string) bool {
				name := filepath.Base(path)
				return !shouldSkipDirName(name)
			},
		}},
		Debounce: debounce,
		Ignore: func(path string) bool {
			rel, err := filepath.Rel(workspace, path)
			if err != nil {
				return true
			}
			rel = filepath.ToSlash(rel)
			return matchesAny(matchers, rel) || strings.HasPrefix(filepath.Base(path), ".DS_Store")
		},
		OnDebounce: func(ctx context.Context) error {
			_, err := m.Refresh(ctx, agentKey, RefreshOptions{Mode: "watcher"})
			return err
		},
	})
	if err != nil {
		cancel()
		log.Printf("[kbase] watcher disabled agent=%s workspace=%s: %v", agentKey, workspace, err)
		return
	}
	m.mu.Lock()
	if existing, ok := m.watchers[agentKey]; ok {
		if existing.cancel != nil {
			existing.cancel()
		}
	}
	m.watchers[agentKey] = watcherBinding{watcher: watcher, cancel: cancel, signature: watcherSignature(spec)}
	m.mu.Unlock()
}

func (m *Manager) kbaseAgentKeys() []string {
	if m == nil || m.agents == nil {
		return nil
	}
	specs := m.agents.Agents()
	keys := make([]string, 0, len(specs))
	for _, spec := range specs {
		if strings.EqualFold(strings.TrimSpace(spec.Mode), Mode) {
			if key := strings.TrimSpace(spec.Key); key != "" {
				keys = append(keys, key)
			}
		}
	}
	return keys
}

func watcherSignature(spec AgentSpec) string {
	payload := struct {
		WorkspaceRoot string
		Config        AgentConfig
	}{
		WorkspaceRoot: strings.TrimSpace(spec.WorkspaceRoot),
		Config:        spec.Config,
	}
	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (m *Manager) Refresh(ctx context.Context, agentKey string, options RefreshOptions) (RefreshResult, error) {
	cfg, embedder, err := m.resolve(agentKey)
	if err != nil {
		return RefreshResult{AgentKey: agentKey, Status: "failed", Error: err.Error()}, err
	}
	storageKey := storageLockKey(cfg.StorageDir)
	lock := m.storageLock(storageKey)
	lock.Lock()
	defer lock.Unlock()
	m.setRunning(cfg.AgentKey, storageKey, true)
	defer m.setRunning(cfg.AgentKey, storageKey, false)

	store, err := OpenStore(cfg.StorageDir)
	if err != nil {
		return RefreshResult{AgentKey: cfg.AgentKey, Status: "failed", Error: err.Error()}, err
	}
	defer store.Close()
	run, err := store.BeginRun(firstNonBlank(options.Mode, "manual"))
	if err != nil {
		return RefreshResult{AgentKey: cfg.AgentKey, Status: "failed", Error: err.Error()}, err
	}
	status := "success"
	errText := ""
	if err = indexWorkspace(ctx, store, cfg, embedder, options.Force, &run); err != nil {
		status = "failed"
		errText = err.Error()
	}
	_ = store.FinishRun(run, status, errText)
	result := RefreshResult{
		AgentKey:      cfg.AgentKey,
		Mode:          run.Mode,
		Status:        status,
		ScannedFiles:  run.ScannedFiles,
		ChangedFiles:  run.ChangedFiles,
		DeletedFiles:  run.DeletedFiles,
		IndexedChunks: run.IndexedChunks,
		Error:         errText,
	}
	return result, err
}

func (m *Manager) Status(agentKey string) (Status, error) {
	cfg, _, err := m.resolve(agentKey)
	if err != nil {
		return Status{AgentKey: agentKey, Mode: Mode}, err
	}
	status := Status{
		AgentKey:        cfg.AgentKey,
		Mode:            Mode,
		StorageLocation: cfg.Storage,
		StorageDir:      cfg.StorageDir,
		WorkspaceRoot:   cfg.WorkspaceRoot,
		Embedding:       cfg.Embedding,
		Chunk:           cfg.Chunk,
		Indexing:        m.isIndexing(cfg.AgentKey, cfg.StorageDir),
		ConfigHash:      cfg.ConfigHash,
	}
	store, err := OpenReadStore(cfg.StorageDir)
	if err != nil {
		if os.IsNotExist(err) {
			status.Stale = true
			return status, nil
		}
		return status, err
	}
	defer store.Close()
	files, chunks, err := store.Counts()
	if err != nil {
		return status, err
	}
	status.Files = files
	status.Chunks = chunks
	if stats, err := store.FileStats(); err == nil {
		status.FileStats = stats
	}
	status.ManifestConfigHash = store.Meta("configHash")
	status.Stale = status.ManifestConfigHash == "" || status.ManifestConfigHash != cfg.ConfigHash
	if lastIndexed := store.Meta("lastIndexedAt"); lastIndexed != "" {
		status.LastIndexedAt, _ = strconv.ParseInt(lastIndexed, 10, 64)
	}
	status.LastRun = store.LastRun()
	return status, nil
}

func (m *Manager) Search(ctx context.Context, agentKey string, query string, options SearchOptions) (SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return SearchResult{}, fmt.Errorf("query must not be blank")
	}
	cfg, embedder, err := m.resolve(agentKey)
	if err != nil {
		return SearchResult{}, err
	}
	status, statusErr := m.Status(agentKey)
	limit := options.Limit
	if limit <= 0 {
		limit = cfg.Retrieval.TopK
	}
	if limit <= 0 {
		limit = 8
	}
	if limit > 50 {
		limit = 50
	}
	offset := options.Offset
	if offset < 0 {
		offset = 0
	}
	scope := newPathScope(options.PathPrefix, options.PathGlob, options.Type)
	store, err := OpenReadStore(cfg.StorageDir)
	if err != nil {
		if os.IsNotExist(err) {
			if statusErr == nil && status.Stale {
				m.queueRefresh(cfg.AgentKey, cfg.StorageDir, "search")
			}
			return SearchResult{
				AgentKey: cfg.AgentKey,
				Query:    query,
				Count:    0,
				Offset:   offset,
				Limit:    limit,
				Results:  nil,
				Stale:    true,
				Indexing: statusErr == nil && status.Indexing,
			}, nil
		}
		return SearchResult{}, err
	}
	defer store.Close()
	queryVector, err := embedder.EmbedSingle(ctx, query)
	if err != nil {
		return SearchResult{}, err
	}
	ftsLimit := (offset + limit) * 4
	fts, err := store.SearchFTS(query, scope, ftsLimit)
	if err != nil {
		return SearchResult{}, err
	}
	chunks, err := store.AllChunksWithEmbeddings(scope)
	if err != nil {
		return SearchResult{}, err
	}
	ftsScores := map[string]float64{}
	for _, hit := range fts {
		ftsScores[hit.Chunk.ID] = hit.Score
	}
	vectorScores := map[string]float64{}
	chunksByID := map[string]chunkRecord{}
	for _, chunk := range chunks {
		chunksByID[chunk.ID] = chunk
		vectorScores[chunk.ID] = cosineSimilarity(queryVector, chunk.Embedding)
	}
	for _, hit := range fts {
		if _, ok := chunksByID[hit.Chunk.ID]; !ok {
			chunksByID[hit.Chunk.ID] = hit.Chunk
		}
	}
	hits := make([]SearchHit, 0, len(chunksByID))
	for id, chunk := range chunksByID {
		vectorScore := vectorScores[id]
		ftsScore := ftsScores[id]
		score := cfg.Retrieval.VectorWeight*vectorScore + cfg.Retrieval.FTSWeight*ftsScore
		if score <= 0 {
			continue
		}
		matchType := "hybrid"
		if vectorScore > 0 && ftsScore == 0 {
			matchType = "vector"
		} else if vectorScore == 0 && ftsScore > 0 {
			matchType = "fts"
		}
		hits = append(hits, SearchHit{
			ChunkID:    chunk.ID,
			Path:       chunk.Path,
			Heading:    chunk.Heading,
			StartLine:  chunk.StartLine,
			EndLine:    chunk.EndLine,
			PageStart:  chunk.PageStart,
			PageEnd:    chunk.PageEnd,
			SlideStart: chunk.SlideStart,
			SlideEnd:   chunk.SlideEnd,
			SourceType: chunk.SourceType,
			Snippet:    snippet(chunk.Content, query),
			Score:      score,
			MatchType:  matchType,
		})
	}
	hits = sortedSearchHits(hits, 0)
	matchCount := len(hits)
	hits, truncated := pageSearchHits(hits, offset, limit)
	result := SearchResult{
		AgentKey:   cfg.AgentKey,
		Query:      query,
		Count:      len(hits),
		MatchCount: matchCount,
		Offset:     offset,
		Limit:      limit,
		Truncated:  truncated,
		Results:    hits,
		Stale:      statusErr == nil && status.Stale,
		Indexing:   statusErr == nil && status.Indexing,
	}
	if result.Stale && !result.Indexing {
		m.queueRefresh(cfg.AgentKey, cfg.StorageDir, "search")
	}
	return result, nil
}

func (m *Manager) Read(agentKey string, options ReadOptions) (ReadResult, error) {
	cfg, _, err := m.resolve(agentKey)
	if err != nil {
		return ReadResult{}, err
	}
	chunkID := strings.TrimSpace(options.ChunkID)
	path := filepath.ToSlash(strings.TrimSpace(options.Path))
	if chunkID == "" && path == "" {
		return ReadResult{}, fmt.Errorf("chunkId or path is required")
	}
	store, err := OpenReadStore(cfg.StorageDir)
	if err != nil {
		if os.IsNotExist(err) {
			return ReadResult{Found: false, ChunkID: chunkID, Path: path}, nil
		}
		return ReadResult{}, err
	}
	defer store.Close()
	if chunkID != "" {
		chunk, err := store.ReadChunk(chunkID)
		if err != nil || chunk == nil {
			if err != nil {
				return ReadResult{}, err
			}
			return ReadResult{Found: false, ChunkID: chunkID}, nil
		}
		return ReadResult{
			Found:      true,
			ChunkID:    chunk.ID,
			Path:       chunk.Path,
			Heading:    chunk.Heading,
			StartLine:  chunk.StartLine,
			EndLine:    chunk.EndLine,
			PageStart:  chunk.PageStart,
			PageEnd:    chunk.PageEnd,
			SlideStart: chunk.SlideStart,
			SlideEnd:   chunk.SlideEnd,
			SourceType: chunk.SourceType,
			Content:    chunk.Content,
		}, nil
	}
	result, err := store.ReadPath(path, options.Offset, options.Limit)
	if err != nil {
		return ReadResult{}, err
	}
	return *result, nil
}

func (m *Manager) resolve(agentKey string) (resolvedConfig, *Embedder, error) {
	if m == nil || m.agents == nil || m.models == nil {
		return resolvedConfig{}, nil, &PolicyError{Kind: ErrorUnavailable, Message: "kbase manager not configured"}
	}
	def, err := m.agentSpec(agentKey)
	if err != nil {
		return resolvedConfig{}, nil, err
	}
	agentKey = strings.TrimSpace(agentKey)
	workspace := strings.TrimSpace(def.WorkspaceRoot)
	if workspace == "" || strings.EqualFold(workspace, WorkspaceRootChat) {
		return resolvedConfig{}, nil, fmt.Errorf("agent %s runtimeConfig.workspaceRoot is required for KBASE", agentKey)
	}
	if !filepath.IsAbs(workspace) {
		return resolvedConfig{}, nil, fmt.Errorf("agent %s runtimeConfig.workspaceRoot must be an absolute path for KBASE", agentKey)
	}
	embedding, provider, err := m.resolveEmbedding(agentKey, def.Config.Embedding)
	if err != nil {
		return resolvedConfig{}, nil, err
	}
	baseURL := strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/")
	if baseURL == "" || embedding.Model == "" || embedding.Dimension <= 0 {
		return resolvedConfig{}, nil, fmt.Errorf("provider %s embedding requires baseUrl/model/dimension", provider.Key)
	}
	storage := strings.ToLower(strings.TrimSpace(def.Config.Storage.Location))
	if storage == "" {
		storage = "runtime"
	}
	var storageDir string
	switch storage {
	case "runtime":
		storageDir = filepath.Join(m.options.RuntimeDir, def.Key)
	case "workspace":
		storageDir = filepath.Join(workspace, ".kbase")
	default:
		return resolvedConfig{}, nil, fmt.Errorf("kbaseConfig.storage.location must be runtime or workspace")
	}
	cfg := resolvedConfig{
		AgentKey:      def.Key,
		WorkspaceRoot: workspace,
		StorageDir:    storageDir,
		Storage:       storage,
		Embedding:     embedding,
		Include:       append([]string(nil), def.Config.Include...),
		Exclude:       append([]string(nil), def.Config.Exclude...),
		Chunk:         NormalizeChunkConfig(def.Config.Chunk),
		Retrieval:     def.Config.Retrieval,
		Extraction:    m.options.Extraction,
		Support:       m.support,
	}
	cfg.ConfigHash = computeConfigHash(cfg)
	embedder := NewEmbedder(baseURL, provider.APIKey, embedding.Model, embedding.Dimension, embedding.Timeout)
	if strings.TrimSpace(embedding.EndpointPath) != "" {
		embedder.EndpointPath = embedding.EndpointPath
	}
	return cfg, embedder, nil
}

func (m *Manager) agentSpec(agentKey string) (AgentSpec, error) {
	if m == nil || m.agents == nil {
		return AgentSpec{}, &PolicyError{Kind: ErrorUnavailable, Message: "kbase manager not configured"}
	}
	agentKey = strings.TrimSpace(agentKey)
	def, ok := m.agents.Agent(agentKey)
	if !ok {
		return AgentSpec{}, &PolicyError{Kind: ErrorNotFound, Message: fmt.Sprintf("agent %s not found", agentKey)}
	}
	if !strings.EqualFold(strings.TrimSpace(def.Mode), Mode) {
		return AgentSpec{}, &PolicyError{Kind: ErrorWrongMode, Message: fmt.Sprintf("agent %s is not mode: KBASE", agentKey)}
	}
	return def, nil
}

func (m *Manager) resolveEmbedding(agentKey string, agentEmbedding EmbeddingConfig) (EmbeddingSnapshot, models.ProviderDefinition, error) {
	modelKey := firstNonBlank(agentEmbedding.ModelKey, m.options.DefaultEmbeddingModelKey)
	if modelKey == "" {
		return EmbeddingSnapshot{}, models.ProviderDefinition{}, fmt.Errorf("agent %s kbaseConfig.embedding.modelKey is required", agentKey)
	}
	model, provider, err := m.models.GetEmbedding(modelKey)
	if err != nil {
		return EmbeddingSnapshot{}, models.ProviderDefinition{}, err
	}
	embedding := EmbeddingSnapshot{
		ModelKey:     model.Key,
		ProviderKey:  provider.Key,
		Model:        model.ModelID,
		Dimension:    model.Embedding.Dimension,
		Timeout:      firstPositive(model.Embedding.Timeout, provider.Embedding.Timeout, 15),
		EndpointPath: strings.TrimSpace(model.Embedding.EndpointPath),
	}
	return embedding, provider, nil
}

func (m *Manager) storageLock(storageKey string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	lock := m.locks[storageKey]
	if lock == nil {
		lock = &sync.Mutex{}
		m.locks[storageKey] = lock
	}
	return lock
}

func (m *Manager) setRunning(agentKey string, storageKey string, running bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if running {
		m.running[agentKey] = true
		m.storageRunning[storageKey] = true
	} else {
		delete(m.running, agentKey)
		delete(m.storageRunning, storageKey)
	}
}

func (m *Manager) isIndexing(agentKey string, storageDir string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	storageKey := storageLockKey(storageDir)
	return m.running[agentKey] || m.storageRunning[storageKey] || m.storageQueued[storageKey]
}

func (m *Manager) queueRefresh(agentKey string, storageDir string, mode string) {
	storageKey := storageLockKey(storageDir)
	m.mu.Lock()
	if m.storageRunning[storageKey] || m.storageQueued[storageKey] {
		m.mu.Unlock()
		return
	}
	m.storageQueued[storageKey] = true
	m.mu.Unlock()

	go func() {
		defer m.setStorageQueued(storageKey, false)
		if _, err := m.Refresh(context.Background(), agentKey, RefreshOptions{Mode: mode}); err != nil {
			log.Printf("[kbase] %s refresh failed agent=%s: %v", mode, agentKey, err)
		}
	}()
}

func (m *Manager) setStorageQueued(storageKey string, queued bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if queued {
		m.storageQueued[storageKey] = true
	} else {
		delete(m.storageQueued, storageKey)
	}
}

func storageLockKey(storageDir string) string {
	storageDir = filepath.Clean(strings.TrimSpace(storageDir))
	if storageDir == "." || storageDir == "" {
		return storageDir
	}
	if abs, err := filepath.Abs(storageDir); err == nil {
		return abs
	}
	return storageDir
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func snippet(content string, query string) string {
	const maxSnippet = 500
	content = strings.TrimSpace(content)
	if len([]rune(content)) <= maxSnippet {
		return content
	}
	lowerContent := strings.ToLower(content)
	index := -1
	for _, term := range strings.Fields(strings.ToLower(query)) {
		if i := strings.Index(lowerContent, term); i >= 0 {
			index = i
			break
		}
	}
	runes := []rune(content)
	if index < 0 {
		return string(runes[:maxSnippet])
	}
	prefix := len([]rune(content[:index]))
	start := prefix - maxSnippet/3
	if start < 0 {
		start = 0
	}
	end := start + maxSnippet
	if end > len(runes) {
		end = len(runes)
	}
	out := strings.TrimSpace(string(runes[start:end]))
	if start > 0 {
		out = "..." + out
	}
	if end < len(runes) {
		out += "..."
	}
	return out
}
