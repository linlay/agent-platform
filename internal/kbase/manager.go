package kbase

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/models"
	runtimewatch "agent-platform/internal/watch"
)

type Manager struct {
	cfg      config.Config
	registry catalog.Registry
	models   *models.ModelRegistry

	mu             sync.Mutex
	locks          map[string]*sync.Mutex
	watchers       map[string]*runtimewatch.Watcher
	running        map[string]bool
	storageRunning map[string]bool
	storageQueued  map[string]bool
}

func NewManager(cfg config.Config, registry catalog.Registry, modelRegistry *models.ModelRegistry) *Manager {
	return &Manager{
		cfg:            cfg,
		registry:       registry,
		models:         modelRegistry,
		locks:          map[string]*sync.Mutex{},
		watchers:       map[string]*runtimewatch.Watcher{},
		running:        map[string]bool{},
		storageRunning: map[string]bool{},
		storageQueued:  map[string]bool{},
	}
}

func (m *Manager) Start(ctx context.Context) {
	if m == nil {
		return
	}
	m.ensureWatchers(ctx)
	for _, key := range m.kbaseAgentKeys() {
		agentKey := key
		go func() {
			if _, err := m.Refresh(ctx, agentKey, RefreshOptions{Mode: "startup"}); err != nil {
				log.Printf("[kbase] startup refresh failed agent=%s: %v", agentKey, err)
			}
		}()
	}
	interval := m.cfg.KBase.Refresh.ReconcileInterval
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

func (m *Manager) ensureWatchers(ctx context.Context) {
	for _, key := range m.kbaseAgentKeys() {
		def, ok := m.registry.AgentDefinition(key)
		if !ok {
			continue
		}
		workspace := strings.TrimSpace(def.Workspace.Root)
		if workspace == "" {
			continue
		}
		m.mu.Lock()
		_, exists := m.watchers[key]
		m.mu.Unlock()
		if exists {
			continue
		}
		m.startWatcher(ctx, key, workspace)
	}
}

func (m *Manager) startWatcher(ctx context.Context, agentKey string, workspace string) {
	debounce := m.cfg.KBase.Refresh.Debounce
	if debounce <= 0 {
		debounce = 2 * time.Second
	}
	watcher, err := runtimewatch.Start(ctx, runtimewatch.Spec{
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
			return matchesAny(compileMatchers(defaultExcludes()), rel) || strings.HasPrefix(filepath.Base(path), ".DS_Store")
		},
		OnDebounce: func(ctx context.Context) error {
			_, err := m.Refresh(ctx, agentKey, RefreshOptions{Mode: "watcher"})
			return err
		},
	})
	if err != nil {
		log.Printf("[kbase] watcher disabled agent=%s workspace=%s: %v", agentKey, workspace, err)
		return
	}
	m.mu.Lock()
	m.watchers[agentKey] = watcher
	m.mu.Unlock()
}

func (m *Manager) kbaseAgentKeys() []string {
	if m == nil || m.registry == nil {
		return nil
	}
	summaries := m.registry.Agents("all")
	keys := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		if strings.EqualFold(strings.TrimSpace(summary.Mode), catalog.AgentModeKBase) {
			keys = append(keys, strings.TrimSpace(summary.Key))
		}
	}
	return keys
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
		return Status{AgentKey: agentKey, Mode: "KBASE"}, err
	}
	status := Status{
		AgentKey:        cfg.AgentKey,
		Mode:            "KBASE",
		StorageLocation: cfg.Storage,
		StorageDir:      cfg.StorageDir,
		WorkspaceRoot:   cfg.WorkspaceRoot,
		Embedding:       cfg.Embedding,
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
	fts, err := store.SearchFTS(query, limit*4)
	if err != nil {
		return SearchResult{}, err
	}
	chunks, err := store.AllChunksWithEmbeddings()
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
	hits = sortedSearchHits(hits, limit)
	result := SearchResult{
		AgentKey: cfg.AgentKey,
		Query:    query,
		Count:    len(hits),
		Results:  hits,
		Stale:    statusErr == nil && status.Stale,
		Indexing: statusErr == nil && status.Indexing,
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
	if m == nil || m.registry == nil || m.models == nil {
		return resolvedConfig{}, nil, fmt.Errorf("kbase manager not configured")
	}
	agentKey = strings.TrimSpace(agentKey)
	def, ok := m.registry.AgentDefinition(agentKey)
	if !ok {
		return resolvedConfig{}, nil, fmt.Errorf("agent %s not found", agentKey)
	}
	if !strings.EqualFold(strings.TrimSpace(def.Mode), catalog.AgentModeKBase) {
		return resolvedConfig{}, nil, fmt.Errorf("agent %s is not mode: KBASE", agentKey)
	}
	workspace := strings.TrimSpace(def.Workspace.Root)
	if workspace == "" || strings.EqualFold(workspace, catalog.AgentWorkspaceRootChat) {
		return resolvedConfig{}, nil, fmt.Errorf("agent %s runtimeConfig.workspaceRoot is required for KBASE", agentKey)
	}
	if !filepath.IsAbs(workspace) {
		return resolvedConfig{}, nil, fmt.Errorf("agent %s runtimeConfig.workspaceRoot must be an absolute path for KBASE", agentKey)
	}
	providerKey := strings.TrimSpace(def.KBaseConfig.Embedding.ProviderKey)
	if providerKey == "" {
		return resolvedConfig{}, nil, fmt.Errorf("agent %s kbaseConfig.embedding.providerKey is required", agentKey)
	}
	provider, err := m.models.GetProvider(providerKey)
	if err != nil {
		return resolvedConfig{}, nil, err
	}
	embedding := EmbeddingSnapshot{
		ProviderKey: providerKey,
		Model:       firstNonBlank(def.KBaseConfig.Embedding.Model, provider.Embedding.Model),
		Dimension:   firstPositive(def.KBaseConfig.Embedding.Dimension, provider.Embedding.Dimension),
		Timeout:     firstPositive(def.KBaseConfig.Embedding.Timeout, provider.Embedding.Timeout, 15),
	}
	baseURL := strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/")
	if baseURL == "" || embedding.Model == "" || embedding.Dimension <= 0 {
		return resolvedConfig{}, nil, fmt.Errorf("provider %s embedding requires baseUrl/model/dimension", providerKey)
	}
	storage := strings.ToLower(strings.TrimSpace(def.KBaseConfig.Storage.Location))
	if storage == "" {
		storage = "runtime"
	}
	var storageDir string
	switch storage {
	case "runtime":
		storageDir = filepath.Join(m.cfg.Paths.KBaseDir, def.Key)
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
		Include:       append([]string(nil), def.KBaseConfig.Include...),
		Exclude:       append([]string(nil), def.KBaseConfig.Exclude...),
		Chunk:         def.KBaseConfig.Chunk,
		Retrieval:     def.KBaseConfig.Retrieval,
		Extraction:    m.cfg.KBase.Extraction,
	}
	cfg.ConfigHash = computeConfigHash(cfg)
	return cfg, NewEmbedder(baseURL, provider.APIKey, embedding.Model, embedding.Dimension, embedding.Timeout), nil
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
