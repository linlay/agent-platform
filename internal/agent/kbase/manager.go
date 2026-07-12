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
	engine  *LanceEngineProcess
	lance   *LanceRetrievalStore

	mu               sync.Mutex
	refreshWG        sync.WaitGroup
	watchReconcileMu sync.Mutex
	watchContext     context.Context
	locks            map[string]*sync.Mutex
	watchers         map[string]watcherBinding
	running          map[string]bool
	storageRunning   map[string]bool
	storageQueued    map[string]bool
	migrationSem     chan struct{}
	shadowQueries    map[string]int
	closing          bool
}

type ManagerOptions struct {
	RuntimeDir               string
	DefaultEmbeddingModelKey string
	RefreshDebounce          time.Duration
	ReconcileInterval        time.Duration
	Extraction               ExtractionConfig
	StorageEngine            string
	Migration                MigrationOptions
	Index                    IndexOptions
	Maintenance              MaintenanceOptions
}

type MigrationOptions struct {
	Enabled           bool
	MaxConcurrency    int
	RetainLegacy      bool
	ShadowLivePercent int
	MaxReplayQueries  int
}

type IndexOptions struct {
	FTSBaseTokenizer string
	ANNMinRows       int
}

type MaintenanceOptions struct {
	OptimizeChangeThreshold int
	OptimizeInterval        time.Duration
	VersionRetention        time.Duration
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
	watcher    *runtimewatch.Watcher
	cancel     context.CancelFunc
	signature  string
	agentKey   string
	storageDir string
}

func NewManager(options ManagerOptions, agents AgentSource, modelRegistry *models.ModelRegistry) *Manager {
	engine := NewLanceEngineProcess(nil)
	migrationConcurrency := options.Migration.MaxConcurrency
	if migrationConcurrency <= 0 {
		migrationConcurrency = 1
	}
	return &Manager{
		options:        options,
		agents:         agents,
		models:         modelRegistry,
		engine:         engine,
		lance:          NewLanceRetrievalStore(engine),
		locks:          map[string]*sync.Mutex{},
		watchers:       map[string]watcherBinding{},
		running:        map[string]bool{},
		storageRunning: map[string]bool{},
		storageQueued:  map[string]bool{},
		migrationSem:   make(chan struct{}, migrationConcurrency),
		shadowQueries:  map[string]int{},
	}
}

// ValidateConfiguration enforces the one-agent-per-canonical-storage rule
// before background refreshers start.
func (m *Manager) ValidateConfiguration() error {
	if m == nil || m.agents == nil {
		return nil
	}
	owners := map[string]string{}
	for _, spec := range m.agents.Agents() {
		if !strings.EqualFold(strings.TrimSpace(spec.Mode), Mode) {
			continue
		}
		storage := strings.ToLower(strings.TrimSpace(spec.Config.Storage.Location))
		if storage == "" {
			storage = "runtime"
		}
		var root string
		switch storage {
		case "runtime":
			root = filepath.Join(m.options.RuntimeDir, spec.Key)
		case "workspace":
			root = filepath.Join(strings.TrimSpace(spec.WorkspaceRoot), ".kbase")
		default:
			continue
		}
		canonical := storageLockKey(root)
		if resolved, err := filepath.EvalSymlinks(canonical); err == nil {
			canonical = filepath.Clean(resolved)
		}
		if owner, exists := owners[canonical]; exists && owner != spec.Key {
			return fmt.Errorf("KBASE storageDir %s is shared by agents %s and %s; each canonical storageDir must have exactly one owner", canonical, owner, spec.Key)
		}
		owners[canonical] = spec.Key
	}
	return nil
}

// ProbeSidecar is used by the container health endpoint. SQLite-only
// deployments and runtimes without KBASE agents do not require the helper;
// otherwise this performs the authenticated protocol handshake/health call.
func (m *Manager) ProbeSidecar(ctx context.Context) (required bool, state LanceEngineState, err error) {
	if m == nil || m.engine == nil || m.engineMode() == "sqlite" || len(m.kbaseAgentKeys()) == 0 {
		return false, LanceEngineState{}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := m.engine.EnsureStarted(ctx); err != nil {
		state = m.engine.State()
		return true, state, err
	}
	return true, m.engine.State(), nil
}

func (m *Manager) WithSupportPackages(registry *supportpkg.Registry) *Manager {
	if m == nil {
		return nil
	}
	m.support = registry
	if m.engine != nil {
		m.engine.SetRegistry(registry)
	}
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
	if m.engine != nil {
		m.engine.SetLifecycleContext(ctx)
	}
	for key, binding := range m.watchers {
		if binding.cancel != nil {
			binding.cancel()
		}
		delete(m.watchers, key)
	}
	m.watchContext = ctx
	m.mu.Unlock()
	m.ensureWatchers(ctx)
	if orphans, err := m.AuditOrphanStorage(); err != nil {
		log.Printf("[kbase] orphan storage audit failed: %v", err)
	} else {
		for _, orphan := range orphans {
			log.Printf("[kbase] orphan storage path=%s sizeBytes=%d lastUsedAt=%d possibleOwner=%s",
				orphan.Path, orphan.SizeBytes, orphan.LastUsedAt, orphan.PossibleOwner)
		}
	}
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

// Close stops watchers and the lazily-started Lance sidecar. It is safe when
// KBASE never started the sidecar.
func (m *Manager) Close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	m.closing = true
	for key, binding := range m.watchers {
		if binding.cancel != nil {
			binding.cancel()
		}
		delete(m.watchers, key)
	}
	m.mu.Unlock()
	done := make(chan struct{})
	go func() {
		m.refreshWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		if m.engine != nil {
			_ = m.engine.Stop(context.Background())
		}
		return ctx.Err()
	}
	if m.engine != nil {
		return m.engine.Stop(ctx)
	}
	return nil
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
	var released []watcherBinding
	for key, binding := range m.watchers {
		spec, ok := desired[key]
		if ok && binding.signature == watcherSignature(spec) {
			delete(desired, key)
			continue
		}
		if binding.cancel != nil {
			binding.cancel()
		}
		if !ok || storageLockKey(binding.storageDir) != storageLockKey(m.storageDirForSpec(spec)) {
			released = append(released, binding)
		}
		delete(m.watchers, key)
	}
	m.mu.Unlock()
	for _, binding := range released {
		go m.releaseStorageGeneration(binding.agentKey, binding.storageDir)
	}

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
	m.watchers[agentKey] = watcherBinding{
		watcher: watcher, cancel: cancel, signature: watcherSignature(spec),
		agentKey: agentKey, storageDir: m.storageDirForSpec(spec),
	}
	m.mu.Unlock()
}

func (m *Manager) storageDirForSpec(spec AgentSpec) string {
	if strings.EqualFold(strings.TrimSpace(spec.Config.Storage.Location), "workspace") {
		return filepath.Join(strings.TrimSpace(spec.WorkspaceRoot), ".kbase")
	}
	return filepath.Join(m.options.RuntimeDir, strings.TrimSpace(spec.Key))
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
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		err := &PolicyError{Kind: ErrorUnavailable, Message: "KBASE manager is shutting down"}
		return failedRefresh(agentKey, options.Mode, err), err
	}
	m.refreshWG.Add(1)
	m.mu.Unlock()
	defer m.refreshWG.Done()
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

	return m.refreshResolved(ctx, cfg, embedder, options)
}

func (m *Manager) Status(agentKey string) (Status, error) {
	cfg, _, err := m.resolve(agentKey)
	if err != nil {
		return Status{AgentKey: agentKey, Mode: Mode}, err
	}
	status := Status{
		AgentKey:         cfg.AgentKey,
		Mode:             Mode,
		StorageLocation:  cfg.Storage,
		StorageDir:       cfg.StorageDir,
		WorkspaceRoot:    cfg.WorkspaceRoot,
		Embedding:        cfg.Embedding,
		Chunk:            cfg.Chunk,
		Indexing:         m.isIndexing(cfg.AgentKey, cfg.StorageDir),
		ConfigHash:       desiredIndexHash(cfg),
		LegacyAvailable:  fileExists(filepath.Join(cfg.StorageDir, "kbase.db")),
		StorageDiskUsage: storageDiskUsage(cfg.StorageDir),
	}
	mode := m.engineMode()
	if mode != "sqlite" {
		control, controlErr := OpenReadControlStore(cfg.StorageDir)
		if controlErr == nil {
			defer control.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			active, activeErr := control.ActiveGeneration(ctx)
			if activeErr != nil {
				return status, activeErr
			}
			if migration, migrationErr := control.LatestMigration(ctx, cfg.AgentKey); migrationErr == nil && migration != nil {
				status.Migration = &MigrationStatus{State: migration.State, Progress: migration.Progress,
					ImportedFiles: migration.ImportedFiles, TotalFiles: migration.TotalFiles,
					ImportedChunks: migration.ImportedChunks, TotalChunks: migration.TotalChunks, Error: migration.Error}
			}
			if active != nil {
				status.Engine = "lancedb"
				status.SchemaVersion = ControlSchemaVersion
				status.Generation = &GenerationStatus{ID: active.ID, State: active.State, TableVersion: active.TableVersion,
					CreatedAt: active.CreatedAt, ActivatedAt: active.ActivatedAt}
				status.Files, status.Chunks, _ = generationControlCounts(ctx, control, active.ID)
				status.FileStats, _ = control.FileStats(ctx, active.ID)
				status.ManifestConfigHash = active.IndexHash
				status.Stale = active.IndexHash == "" || active.IndexHash != desiredIndexHash(resolvedLanceConfig(cfg))
				if last, metaErr := control.Meta(ctx, "lastIndexedAt"); metaErr == nil {
					status.LastIndexedAt, _ = strconv.ParseInt(last, 10, 64)
				}
				status.LastRun, _ = control.LastRun(ctx)
				if pending, pendingErr := control.PendingFileOperations(ctx, active.ID); pendingErr == nil {
					status.PendingRecoveryOps = len(pending)
				}
				registerErr := m.registerLanceGeneration(ctx, cfg, active)
				state := m.engine.State()
				if registerErr != nil {
					state.LastError = registerErr.Error()
					state.Available = false
				}
				status.Sidecar = &state
				indexes := &IndexesStatus{}
				if stats, statsErr := m.lance.Stats(ctx, active.ID); statsErr == nil {
					indexes.FTS = IndexStatus{Type: firstNonBlank(stats.FTSIndexType, "FTS/ICU"), Ready: stats.FTSReady}
					indexes.Vector = VectorIndexStatus{Type: firstNonBlank(stats.VectorIndexType, "flat"), Ready: stats.VectorReady, UnindexedRows: stats.UnindexedRows}
					lastOptimized, _ := control.Meta(ctx, "lastOptimizedAt")
					indexes.LastOptimizedAt, _ = strconv.ParseInt(lastOptimized, 10, 64)
				}
				status.Indexes = indexes
				return status, nil
			}
		} else if !os.IsNotExist(controlErr) {
			return status, controlErr
		}
		if mode == "lancedb" {
			status.Engine = "lancedb"
			status.SchemaVersion = ControlSchemaVersion
			status.Stale = true
			state := m.engine.State()
			status.Sidecar = &state
			return status, nil
		}
	}
	status.Engine = "sqlite"
	status.SchemaVersion = schemaVersion
	rollbackRefreshRequired := legacyRefreshRequired(cfg.StorageDir)
	store, err := OpenReadStore(cfg.StorageDir)
	if err != nil {
		if os.IsNotExist(err) {
			status.Stale = true
			return status, nil
		}
		return status, err
	}
	defer store.Close()
	// A writer creates kbase.db before the schema and the first index manifest
	// are fully committed. Treat that short initialization window as a missing
	// index instead of issuing reads against partially-created tables.
	legacyIndexHash := storedIndexHash(store, cfg.StorageDir)
	status.ManifestConfigHash = legacyIndexHash
	if legacyIndexHash == "" {
		status.Stale = true
		return status, nil
	}
	files, chunks, err := store.Counts()
	if err != nil {
		return status, err
	}
	status.Files = files
	status.Chunks = chunks
	if stats, err := store.FileStats(); err == nil {
		status.FileStats = stats
	}
	status.Stale = legacyIndexHash == "" || legacyIndexHash != desiredIndexHash(cfg)
	if rollbackRefreshRequired {
		status.Stale = true
	}
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
	if m.engineMode() == "sqlite" && legacyRefreshRequired(cfg.StorageDir) {
		m.queueRefresh(cfg.AgentKey, cfg.StorageDir, "sqlite-rollback")
		return SearchResult{AgentKey: cfg.AgentKey, Query: query, Offset: offset, Limit: limit,
			Results: nil, Stale: true, Indexing: true}, nil
	}
	// Keep a request that observed an uninitialized legacy index on the stale
	// path. Otherwise it can race the queued refresh, discover the database
	// milliseconds later, and unexpectedly execute a second embedding request.
	if statusErr == nil && status.Engine == "sqlite" && status.ManifestConfigHash == "" {
		m.queueRefresh(cfg.AgentKey, cfg.StorageDir, "search")
		return SearchResult{AgentKey: cfg.AgentKey, Query: query, Offset: offset, Limit: limit,
			Results: nil, Stale: true, Indexing: true}, nil
	}
	retrieval, generationID, available, err := m.selectRetrieval(ctx, cfg)
	if err != nil {
		return SearchResult{}, err
	}
	if !available {
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
	queryEmbedder, queryDimension, err := m.embedderForRetrieval(ctx, cfg, generationID, embedder)
	if err != nil {
		return SearchResult{}, err
	}
	queryVector, err := queryEmbedder.EmbedSingle(ctx, query)
	if err != nil {
		return SearchResult{}, err
	}
	vector, err := float32Vector(queryVector, queryDimension)
	if err != nil {
		return SearchResult{}, err
	}
	retrievalRequest := RetrievalRequest{
		Query: query, Vector: vector, Limit: limit, Offset: offset,
		CandidateFloor: cfg.Retrieval.CandidateFloor, CandidateMultiplier: cfg.Retrieval.CandidateMultiplier,
		CandidateMax: cfg.Retrieval.CandidateMax, RRFK: cfg.Retrieval.RRFK,
		VectorWeight: cfg.Retrieval.VectorWeight, FTSWeight: cfg.Retrieval.FTSWeight,
		PathPrefix: options.PathPrefix, PathGlob: options.PathGlob, Type: options.Type,
	}
	response, err := retrieval.Search(ctx, generationID, retrievalRequest)
	if err != nil {
		return SearchResult{}, err
	}
	hits := make([]SearchHit, 0, len(response.Matches))
	for _, match := range response.Matches {
		chunk := match.Chunk
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
			Score:      match.Score,
			MatchType:  match.MatchType,
		})
	}
	result := SearchResult{
		AgentKey:   cfg.AgentKey,
		Query:      query,
		Count:      len(hits),
		MatchCount: response.MatchCount,
		Offset:     offset,
		Limit:      limit,
		Truncated:  response.Truncated,
		Results:    hits,
		Stale:      statusErr != nil || status.Stale,
		Indexing:   m.isIndexing(cfg.AgentKey, cfg.StorageDir) || statusErr == nil && status.Indexing,
	}
	if result.Stale && !result.Indexing {
		m.queueRefresh(cfg.AgentKey, cfg.StorageDir, "search")
	}
	if generationID != "legacy" {
		m.maybeShadowLiveQuery(cfg, retrievalRequest, response)
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if m.engineMode() == "sqlite" && legacyRefreshRequired(cfg.StorageDir) {
		m.queueRefresh(cfg.AgentKey, cfg.StorageDir, "sqlite-rollback")
		return ReadResult{Found: false, ChunkID: chunkID, Path: path}, nil
	}
	retrieval, generationID, available, err := m.selectRetrieval(ctx, cfg)
	if err != nil {
		return ReadResult{}, err
	}
	if !available {
		return ReadResult{Found: false, ChunkID: chunkID, Path: path}, nil
	}
	if chunkID != "" {
		chunk, err := retrieval.ReadChunk(ctx, generationID, chunkID)
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
	chunks, err := retrieval.ReadPath(ctx, generationID, path, options.Offset, options.Limit)
	if err != nil {
		return ReadResult{}, err
	}
	return readResultFromChunks(path, options.Offset, chunks), nil
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
		FTSTokenizer:  firstNonBlank(m.options.Index.FTSBaseTokenizer, defaultFTSTokenizer),
	}
	cfg.IndexHash = computeIndexHash(cfg)
	cfg.QueryHash = computeQueryHash(cfg)
	cfg.ConfigHash = cfg.IndexHash
	embedder := newEmbedderForSnapshot(baseURL, provider.APIKey, embedding)
	return cfg, embedder, nil
}

func newEmbedderForSnapshot(baseURL, apiKey string, embedding EmbeddingSnapshot) *Embedder {
	embedder := NewEmbedder(baseURL, apiKey, embedding.Model, embedding.Dimension, embedding.Timeout)
	if strings.TrimSpace(embedding.EndpointPath) != "" {
		embedder.EndpointPath = embedding.EndpointPath
	}
	return embedder
}

// embedderForRetrieval binds query vectors to the active generation snapshot,
// not to a newer agent configuration that may currently be building a blue
// generation. This also keeps a rollback generation in its original vector
// space.
func (m *Manager) embedderForRetrieval(ctx context.Context, cfg resolvedConfig, generationID string, fallback *Embedder) (*Embedder, int, error) {
	if generationID == "legacy" || strings.TrimSpace(generationID) == "" {
		return fallback, cfg.Embedding.Dimension, nil
	}
	control, err := OpenReadControlStore(cfg.StorageDir)
	if err != nil {
		return nil, 0, err
	}
	generation, err := control.Generation(ctx, generationID)
	_ = control.Close()
	if err != nil {
		return nil, 0, err
	}
	if generation == nil {
		return nil, 0, &PolicyError{Kind: ErrorUnavailable, Message: "active KBASE generation metadata is missing"}
	}
	if strings.TrimSpace(generation.EmbeddingModelKey) == "" {
		if generation.EmbeddingModel == cfg.Embedding.Model && generation.EmbeddingDimension == cfg.Embedding.Dimension {
			return fallback, generation.EmbeddingDimension, nil
		}
		return nil, 0, &PolicyError{Kind: ErrorUnavailable, Message: "active KBASE generation embedding snapshot is incomplete"}
	}
	embedding, provider, err := m.resolveEmbedding(cfg.AgentKey, EmbeddingConfig{ModelKey: generation.EmbeddingModelKey})
	if err != nil {
		return nil, 0, &PolicyError{Kind: ErrorUnavailable, Message: "active KBASE generation embedding model is unavailable: " + err.Error()}
	}
	if embedding.Model != generation.EmbeddingModel || embedding.Dimension != generation.EmbeddingDimension {
		return nil, 0, &PolicyError{Kind: ErrorUnavailable, Message: "active KBASE generation embedding model definition changed; rebuild before querying"}
	}
	baseURL := strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/")
	if baseURL == "" {
		return nil, 0, &PolicyError{Kind: ErrorUnavailable, Message: "active KBASE generation embedding provider has no base URL"}
	}
	return newEmbedderForSnapshot(baseURL, provider.APIKey, embedding), generation.EmbeddingDimension, nil
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
