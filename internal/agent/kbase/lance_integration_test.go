package kbase

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestLanceSidecarPerformanceGate is intentionally opt-in because the 100k x
// 1024-vector case creates a sizeable temporary Lance dataset. Release CI runs
// it twice with AP_KBASE_LANCE_PERF_ROWS=10000 and 100000 against the staged
// sidecar; ordinary unit test runs skip it.
func TestLanceSidecarPerformanceGate(t *testing.T) {
	rowsText := strings.TrimSpace(os.Getenv("AP_KBASE_LANCE_PERF_ROWS"))
	if rowsText == "" {
		t.Skip("AP_KBASE_LANCE_PERF_ROWS is not set")
	}
	rows, err := strconv.Atoi(rowsText)
	if err != nil || rows != 10000 && rows != 100000 {
		t.Fatalf("AP_KBASE_LANCE_PERF_ROWS must be 10000 or 100000, got %q", rowsText)
	}
	executable := strings.TrimSpace(os.Getenv("AP_KBASE_LANCE_ENGINE"))
	if executable == "" {
		t.Fatal("AP_KBASE_LANCE_ENGINE is required for the performance gate")
	}
	previousLogWriter := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(previousLogWriter) })
	dimension := 1024
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	process := NewLanceEngineProcess(nil)
	process.SetLifecycleContext(ctx)
	store := NewLanceRetrievalStore(process)
	defer process.Stop(context.Background())
	generationID := fmt.Sprintf("kbg_perf_%d", rows)
	if err := store.CreateGeneration(ctx, GenerationSpec{AgentKey: "perf", GenerationID: generationID,
		StorageDir: t.TempDir(), EmbeddingModel: "perf-embedding", EmbeddingDimension: dimension, FTSBaseTokenizer: "icu"}); err != nil {
		t.Fatal(err)
	}
	for start := 0; start < rows; start += 512 {
		end := minInt(start+512, rows)
		batch := make([]chunkRecord, 0, end-start)
		for row := start; row < end; row++ {
			vector := performanceVector(row, dimension)
			content := fmt.Sprintf("performance document %06d 本地知识库", row)
			batch = append(batch, chunkRecord{ID: fmt.Sprintf("chunk-%06d", row), FileID: fmt.Sprintf("file-%06d", row),
				Path: fmt.Sprintf("docs/%06d.md", row), Ordinal: 0, StartLine: 1, EndLine: 1,
				SourceType: "text", Content: content, ContentHash: shaHex([]byte(content)), Embedding: vector,
				EmbeddingModel: "perf-embedding", EmbeddingDimension: dimension, UpdatedAt: int64(row + 1)})
		}
		if err := store.ImportChunks(ctx, generationID, batch); err != nil {
			t.Fatalf("import rows %d..%d: %v", start, end, err)
		}
	}
	buildRequest := struct {
		lanceBaseRequest
		ANNMinRows   int    `json:"annMinRows"`
		FTSTokenizer string `json:"ftsTokenizer"`
	}{lanceBaseRequest: lanceBaseRequest{RequestID: lanceRequestID("perf_index"), AgentKey: "perf", GenerationID: generationID},
		ANNMinRows: 50000, FTSTokenizer: "icu"}
	var buildResponse struct {
		VectorIndexType string   `json:"vectorIndexType"`
		ANNRecallAt10   *float32 `json:"annRecallAt10"`
	}
	if err := process.doJSON(ctx, "POST", "/v1/indexes/build", buildRequest, &buildResponse, 30*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := store.WaitForIndexes(ctx, generationID, 30*time.Minute); err != nil {
		t.Fatal(err)
	}
	stats, err := store.Stats(ctx, generationID)
	if err != nil || !stats.FTSReady || !stats.VectorReady {
		t.Fatalf("performance index stats=%#v err=%v", stats, err)
	}
	if rows == 100000 && (buildResponse.ANNRecallAt10 == nil || *buildResponse.ANNRecallAt10 < .95) {
		if buildResponse.ANNRecallAt10 == nil {
			t.Fatal("100k performance dataset did not report ANN recall")
		}
		t.Fatalf("100k performance dataset did not meet ANN recall@10 >= 0.95: %.3f", *buildResponse.ANNRecallAt10)
	}
	if rows == 100000 && strings.EqualFold(buildResponse.VectorIndexType, "flat") {
		t.Fatal("100k performance dataset met the recall gate but did not retain the ANN index")
	}
	if buildResponse.ANNRecallAt10 == nil {
		t.Logf("%d-row vector index=%s", rows, stats.VectorIndexType)
	} else {
		t.Logf("%d-row vector index=%s recall@10=%.3f", rows, stats.VectorIndexType, *buildResponse.ANNRecallAt10)
	}
	queryVector := make([]float32, dimension)
	for index, value := range performanceClusterVector(777/64, float64(777%64)+0.5, dimension) {
		queryVector[index] = float32(value)
	}
	request := RetrievalRequest{Query: "performance document", Vector: queryVector, Limit: 8, RRFK: 60,
		VectorWeight: .7, FTSWeight: .3, CandidateFloor: 30, CandidateMultiplier: 4, CandidateMax: 500}
	for warmup := 0; warmup < 10; warmup++ {
		if _, err := store.Search(ctx, generationID, request); err != nil {
			t.Fatal(err)
		}
	}
	durations := make([]time.Duration, 100)
	for index := range durations {
		started := time.Now()
		if _, err := store.Search(ctx, generationID, request); err != nil {
			t.Fatal(err)
		}
		durations[index] = time.Since(started)
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p95 := durations[94]
	limit := 200 * time.Millisecond
	if rows == 100000 {
		limit = 500 * time.Millisecond
	}
	if p95 > limit {
		t.Fatalf("%d-row retrieval p95=%s exceeds %s", rows, p95, limit)
	}
	t.Logf("%d-row retrieval p95=%s", rows, p95)
}

// performanceVector models embedding-like semantic clusters instead of
// independent uniformly random points. Each 64-row cluster follows a distinct
// direction around its centroid, giving exact search a stable local ordering.
func performanceVector(row, dimension int) []float64 {
	return performanceClusterVector(row/64, float64(row%64), dimension)
}

func performanceClusterVector(cluster int, memberPosition float64, dimension int) []float64 {
	vector := make([]float64, dimension)
	clusterSeed := uint64(cluster + 1)
	position := (memberPosition - 31.5) / 31.5 * 0.15
	for index := range vector {
		coordinate := uint64(index + 1)
		baseSeed := clusterSeed*0x9e3779b185ebca87 + coordinate*0xc2b2ae3d27d4eb4f
		directionSeed := clusterSeed*0x165667b19e3779f9 + coordinate*0x85ebca77c2b2ae63
		base := float64(int64(baseSeed%20001)-10000) / 10000
		direction := float64(int64(directionSeed%20001)-10000) / 10000
		vector[index] = base + position*direction
	}
	return vector
}

func TestLanceSidecarEndToEnd(t *testing.T) {
	executable := os.Getenv("AP_KBASE_LANCE_ENGINE")
	if executable == "" {
		t.Skip("AP_KBASE_LANCE_ENGINE is not set")
	}
	abs, err := filepath.Abs(executable)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AP_KBASE_LANCE_ENGINE", abs)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	root := t.TempDir()
	spec := GenerationSpec{AgentKey: "docs", GenerationID: "kbg_integration", StorageDir: root,
		EmbeddingModel: "test-model", EmbeddingDimension: 3, FTSBaseTokenizer: "icu"}

	process := NewLanceEngineProcess(nil)
	process.SetLifecycleContext(ctx)
	store := NewLanceRetrievalStore(process)
	t.Cleanup(func() { _ = process.Stop(context.Background()) })
	if err := store.CreateGeneration(ctx, spec); err != nil {
		t.Fatalf("create generation: %v", err)
	}
	chunks := []chunkRecord{
		{ID: "c_apple", FileID: "f_cn", Path: "docs/中文.md", Ordinal: 0, StartLine: 1, EndLine: 2,
			SourceType: "text", Content: "苹果知识库支持中文检索", ContentHash: shaHex([]byte("苹果知识库支持中文检索")),
			Embedding: []float64{1, 0, 0}, EmbeddingModel: "test-model", EmbeddingDimension: 3, UpdatedAt: 1},
		{ID: "c_lance", FileID: "f_en", Path: "docs/lance.txt", Ordinal: 0, StartLine: 1, EndLine: 1,
			SourceType: "text", Content: "Lance vector retrieval", ContentHash: shaHex([]byte("Lance vector retrieval")),
			Embedding: []float64{0, 1, 0}, EmbeddingModel: "test-model", EmbeddingDimension: 3, UpdatedAt: 1},
	}
	if err := store.ImportChunks(ctx, spec.GenerationID, chunks); err != nil {
		t.Fatalf("Arrow import: %v", err)
	}
	if err := store.BuildIndexes(ctx, spec.GenerationID, IndexSpec{FTSBaseTokenizer: "icu", ANNMinRows: 1000, Distance: "cosine"}); err != nil {
		t.Fatalf("build indexes: %v", err)
	}
	if err := store.WaitForIndexes(ctx, spec.GenerationID, 30*time.Second); err != nil {
		t.Fatalf("wait indexes: %v", err)
	}
	validation, err := store.Validate(ctx, spec.GenerationID)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !validation.Ready || validation.Chunks != 2 || validation.ChunkIDDigest != stableIDDigest([]string{"c_apple", "c_lance"}) {
		t.Fatalf("validation = %#v", validation)
	}
	if got, want := validation.FileChunkHashes["f_cn"], chunkValidationSetHash(chunks[:1]); got != want {
		t.Fatalf("file chunk validation hash = %q, want %q", got, want)
	}
	result, err := store.Search(ctx, spec.GenerationID, RetrievalRequest{Query: "苹果知识库", Vector: []float32{1, 0, 0},
		Limit: 8, RRFK: 60, VectorWeight: .7, FTSWeight: .3, CandidateFloor: 30, CandidateMultiplier: 4,
		CandidateMax: 500, PathPrefix: "docs", Type: ".md"})
	if err != nil {
		t.Fatalf("hybrid search: %v", err)
	}
	if len(result.Matches) != 1 || result.Matches[0].Chunk.ID != "c_apple" {
		t.Fatalf("hybrid result = %#v", result)
	}

	replacement := chunkRecord{ID: "c_banana", FileID: "f_cn", Path: "docs/中文.md", Ordinal: 0, StartLine: 1, EndLine: 2,
		SourceType: "text", Content: "香蕉替换苹果", ContentHash: shaHex([]byte("香蕉替换苹果")),
		Embedding: []float64{1, 0, 0}, EmbeddingModel: "test-model", EmbeddingDimension: 3, UpdatedAt: 2}
	if _, err := store.ReplaceFileChunks(ctx, spec.GenerationID, "f_cn", []chunkRecord{replacement}); err != nil {
		t.Fatalf("replace file: %v", err)
	}
	if old, err := store.ReadChunk(ctx, spec.GenerationID, "c_apple"); err != nil || old != nil {
		t.Fatalf("old chunk after replace = %#v err=%v", old, err)
	}
	// Simulate a crash after Lance committed but before the control record was
	// marked committed. Exact sidecar validation should finish the prepared
	// operation without re-running extraction or embedding.
	control, err := OpenControlStore(root)
	if err != nil {
		t.Fatal(err)
	}
	record := fileRecord{ID: "f_cn", Path: "docs/中文.md", Ext: ".md", Status: "active", ChunkCount: 1,
		ChunkSetHash: chunkValidationSetHash([]chunkRecord{replacement}), IndexedAt: time.Now().UnixMilli()}
	recordJSON, _ := json.Marshal(record)
	op := FileOperation{ID: "op_prepared_after_commit", GenerationID: spec.GenerationID, FileID: record.ID, Path: record.Path,
		Operation: FileOperationReplace, DesiredContentHash: record.ChunkSetHash, DesiredRecordJSON: string(recordJSON)}
	if err := control.BeginFileOperation(ctx, op); err != nil {
		t.Fatal(err)
	}
	recoveryManager := &Manager{lance: store}
	if err := recoveryManager.recoverFileOperations(ctx, control, spec.GenerationID); err != nil {
		t.Fatalf("recover prepared committed operation: %v", err)
	}
	if pending, err := control.PendingFileOperations(ctx, spec.GenerationID); err != nil || len(pending) != 0 {
		t.Fatalf("pending after recovery=%#v err=%v", pending, err)
	}
	_ = control.Close()
	if _, err := store.DeleteFileChunks(ctx, spec.GenerationID, "f_en"); err != nil {
		t.Fatalf("delete file: %v", err)
	}
	stats, err := store.Stats(ctx, spec.GenerationID)
	if err != nil || stats.Chunks != 1 || stats.Files != 1 {
		t.Fatalf("stats after mutations = %#v err=%v", stats, err)
	}
	if released, err := store.ReleaseGeneration(ctx, spec.GenerationID, spec.AgentKey); err != nil || !released {
		t.Fatalf("release = %t err=%v", released, err)
	}
	if err := store.CreateGeneration(ctx, spec); err != nil {
		t.Fatalf("re-register generation: %v", err)
	}
	if chunk, err := store.ReadChunk(ctx, spec.GenerationID, "c_banana"); err != nil || chunk == nil {
		t.Fatalf("read after re-register = %#v err=%v", chunk, err)
	}
	if err := process.Stop(ctx); err != nil {
		t.Fatalf("stop first sidecar: %v", err)
	}

	restarted := NewLanceEngineProcess(nil)
	restarted.SetLifecycleContext(ctx)
	reopened := NewLanceRetrievalStore(restarted)
	defer restarted.Stop(context.Background())
	if err := reopened.CreateGeneration(ctx, spec); err != nil {
		t.Fatalf("open generation after process restart: %v", err)
	}
	if chunk, err := reopened.ReadChunk(ctx, spec.GenerationID, "c_banana"); err != nil || chunk == nil {
		t.Fatalf("read after sidecar restart = %#v err=%v", chunk, err)
	}
}

func TestManagerMigratesLegacyWithoutReembeddingChunks(t *testing.T) {
	executable := os.Getenv("AP_KBASE_LANCE_ENGINE")
	if executable == "" {
		t.Skip("AP_KBASE_LANCE_ENGINE is not set")
	}
	abs, err := filepath.Abs(executable)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AP_KBASE_LANCE_ENGINE", abs)
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "alpha.md"), []byte("# Alpha\nalpha migration content"), 0o644); err != nil {
		t.Fatal(err)
	}
	var embeddingRequests atomic.Int64
	models := newKBaseTestModelRegistry(t, root, testEmbeddingHandler(t, &embeddingRequests))
	agent := testKBaseAgent("docs", workspace, "runtime")
	agents := stubAgentSource{agents: map[string]AgentSpec{"docs": agent}}
	runtimeDir := filepath.Join(root, "kbase")

	legacyManager := NewManager(ManagerOptions{RuntimeDir: runtimeDir, StorageEngine: "sqlite"}, agents, models)
	if _, err := legacyManager.Refresh(context.Background(), "docs", RefreshOptions{Mode: "seed"}); err != nil {
		t.Fatalf("seed legacy SQLite: %v", err)
	}
	if got := embeddingRequests.Load(); got != 1 {
		t.Fatalf("legacy seed embedding requests = %d, want 1", got)
	}
	_ = legacyManager.Close(context.Background())
	embeddingRequests.Store(0)

	manager := NewManager(ManagerOptions{
		RuntimeDir: runtimeDir, StorageEngine: "auto",
		Migration:   MigrationOptions{Enabled: true, MaxConcurrency: 1, RetainLegacy: true, MaxReplayQueries: 10},
		Index:       IndexOptions{FTSBaseTokenizer: "icu", ANNMinRows: 1000},
		Maintenance: MaintenanceOptions{OptimizeChangeThreshold: 1000, OptimizeInterval: 24 * time.Hour, VersionRetention: 7 * 24 * time.Hour},
	}, agents, models)
	defer manager.Close(context.Background())
	if _, err := manager.Refresh(context.Background(), "docs", RefreshOptions{Mode: "migration"}); err != nil {
		t.Fatalf("migrate to Lance: %v", err)
	}
	if got := embeddingRequests.Load(); got != 0 {
		t.Fatalf("migration called embedding provider %d times; unchanged legacy chunks must import directly", got)
	}
	status, err := manager.Status("docs")
	if err != nil {
		t.Fatalf("Lance status: %v", err)
	}
	if status.Engine != "lancedb" || status.Generation == nil || status.Generation.State != GenerationActive || status.Files != 1 || status.Chunks != 1 {
		t.Fatalf("unexpected migrated status: %#v", status)
	}
	result, err := manager.Search(context.Background(), "docs", "alpha", SearchOptions{Limit: 8})
	if err != nil || len(result.Results) != 1 || result.Results[0].Path != "alpha.md" {
		t.Fatalf("search after migration = %#v err=%v", result, err)
	}

	if err := os.WriteFile(filepath.Join(workspace, "alpha.md"), []byte("# Beta\nbeta incrementally replaced"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Refresh(context.Background(), "docs", RefreshOptions{Mode: "incremental"}); err != nil {
		t.Fatalf("incremental Lance refresh: %v", err)
	}
	beta, err := manager.Search(context.Background(), "docs", "beta", SearchOptions{Limit: 8})
	if err != nil || len(beta.Results) != 1 || beta.Results[0].Path != "alpha.md" {
		t.Fatalf("search after replace = %#v err=%v", beta, err)
	}
	beforeForce, _ := manager.Status("docs")
	if _, err := manager.Refresh(context.Background(), "docs", RefreshOptions{Mode: "force", Force: true}); err != nil {
		t.Fatalf("force generation build: %v", err)
	}
	afterForce, _ := manager.Status("docs")
	if beforeForce.Generation == nil || afterForce.Generation == nil || beforeForce.Generation.ID == afterForce.Generation.ID {
		t.Fatalf("force refresh did not blue/green switch: before=%#v after=%#v", beforeForce.Generation, afterForce.Generation)
	}
	rolledBack, err := manager.RollbackGeneration(context.Background(), "docs", beforeForce.Generation.ID)
	if err != nil || rolledBack.ID != beforeForce.Generation.ID {
		t.Fatalf("generation rollback = %#v err=%v", rolledBack, err)
	}
}

func TestManagerResumesPartialIdempotentMigration(t *testing.T) {
	executable := os.Getenv("AP_KBASE_LANCE_ENGINE")
	if executable == "" {
		t.Skip("AP_KBASE_LANCE_ENGINE is not set")
	}
	abs, err := filepath.Abs(executable)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AP_KBASE_LANCE_ENGINE", abs)
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"alpha.md": "alpha resume", "beta.md": "beta resume"} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var embeddingRequests atomic.Int64
	models := newKBaseTestModelRegistry(t, root, testEmbeddingHandler(t, &embeddingRequests))
	agent := testKBaseAgent("docs", workspace, "runtime")
	agents := stubAgentSource{agents: map[string]AgentSpec{"docs": agent}}
	runtimeDir := filepath.Join(root, "kbase")
	legacyManager := NewManager(ManagerOptions{RuntimeDir: runtimeDir, StorageEngine: "sqlite"}, agents, models)
	if _, err := legacyManager.Refresh(context.Background(), "docs", RefreshOptions{Mode: "seed"}); err != nil {
		t.Fatal(err)
	}
	_ = legacyManager.Close(context.Background())
	embeddingRequests.Store(0)

	manager := NewManager(ManagerOptions{RuntimeDir: runtimeDir, StorageEngine: "auto",
		Migration: MigrationOptions{Enabled: true, MaxConcurrency: 1, RetainLegacy: true, MaxReplayQueries: 10},
		Index:     IndexOptions{FTSBaseTokenizer: "icu", ANNMinRows: 1000}}, agents, models)
	defer manager.Close(context.Background())
	cfg, _, err := manager.resolve("docs")
	if err != nil {
		t.Fatal(err)
	}
	cfg = resolvedLanceConfig(cfg)
	legacy, err := OpenStore(cfg.StorageDir)
	if err != nil {
		t.Fatal(err)
	}
	generation := newGeneration(cfg)
	migration := Migration{ID: "kbm_resume", AgentKey: cfg.AgentKey, SourceEngine: "sqlite", SourceSchema: schemaVersion,
		GenerationID: generation.ID, State: MigrationFailedRetryable, TotalFiles: 2, TotalChunks: 2,
		ImportedFiles: 1, ImportedChunks: 1, Progress: .5, StartedAt: time.Now().UnixMilli(), ErrorCode: "engine_internal"}
	control, err := OpenControlStore(cfg.StorageDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.CreateGeneration(context.Background(), generation); err != nil {
		t.Fatal(err)
	}
	if err := control.BeginMigration(context.Background(), migration); err != nil {
		t.Fatal(err)
	}
	snapshotPath := filepath.Join(cfg.StorageDir, "migrations", migration.ID+".snapshot.db")
	if err := legacy.Snapshot(context.Background(), snapshotPath); err != nil {
		t.Fatal(err)
	}
	_ = legacy.Close()
	snapshot, err := OpenSnapshotStore(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := snapshot.AllChunks()
	_ = snapshot.Close()
	if err != nil || len(chunks) != 2 {
		t.Fatalf("snapshot chunks=%d err=%v", len(chunks), err)
	}
	if err := manager.registerLanceGeneration(context.Background(), cfg, &generation); err != nil {
		t.Fatal(err)
	}
	if err := manager.lance.ImportChunks(context.Background(), generation.ID, chunks[:1]); err != nil {
		t.Fatal(err)
	}
	_ = control.Close()
	if err := manager.engine.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, err := manager.Refresh(context.Background(), "docs", RefreshOptions{Mode: "resume"}); err != nil {
		t.Fatalf("resume migration: %v", err)
	}
	if got := embeddingRequests.Load(); got != 0 {
		t.Fatalf("resumed migration called embedding provider %d times", got)
	}
	status, err := manager.Status("docs")
	if err != nil || status.Engine != "lancedb" || status.Chunks != 2 || status.Migration == nil || status.Migration.State != MigrationActive {
		t.Fatalf("resumed status=%#v err=%v", status, err)
	}
}
