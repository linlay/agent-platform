package kbase

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (m *Manager) refreshResolved(ctx context.Context, cfg resolvedConfig, embedder *Embedder, options RefreshOptions) (RefreshResult, error) {
	control, err := OpenControlStore(cfg.StorageDir)
	if err != nil {
		return failedRefresh(cfg.AgentKey, options.Mode, err), err
	}
	defer control.Close()
	active, err := control.ActiveGeneration(ctx)
	if err != nil {
		return failedRefresh(cfg.AgentKey, options.Mode, err), err
	}
	if active != nil {
		if options.Force || active.IndexHash != desiredIndexHash(cfg) || active.EmbeddingDimension != cfg.Embedding.Dimension || active.EmbeddingModel != cfg.Embedding.Model {
			return m.buildLanceGeneration(ctx, control, cfg, embedder, options)
		}
		return m.refreshLanceGeneration(ctx, control, active, cfg, embedder, options)
	}
	return m.buildLanceGeneration(ctx, control, cfg, embedder, options)
}

func (m *Manager) refreshLanceGeneration(ctx context.Context, control *ControlStore, generation *Generation, cfg resolvedConfig, embedder *Embedder, options RefreshOptions) (RefreshResult, error) {
	if err := m.registerLanceGeneration(ctx, cfg, generation); err != nil {
		return failedRefresh(cfg.AgentKey, options.Mode, err), err
	}
	if err := m.recoverFileOperations(ctx, control, generation.ID); err != nil {
		return failedRefresh(cfg.AgentKey, options.Mode, err), err
	}
	if err := control.preparePendingRecovery(ctx, generation.ID); err != nil {
		return failedRefresh(cfg.AgentKey, options.Mode, err), err
	}
	run, err := control.BeginRun(ctx, firstNonBlank(options.Mode, "manual"), generation.ID)
	if err != nil {
		return failedRefresh(cfg.AgentKey, options.Mode, err), err
	}
	indexStore := newLanceIndexStore(ctx, control, m.lance, generation.ID)
	err = indexWorkspace(ctx, indexStore, cfg, embedder, false, &run)
	status, errText := "success", ""
	if err != nil {
		status, errText = "failed", err.Error()
	} else if run.ChangedFiles > 0 || run.DeletedFiles > 0 {
		if buildErr := m.lance.BuildIndexes(ctx, generation.ID, m.indexSpec()); buildErr != nil {
			err, status, errText = buildErr, "failed", buildErr.Error()
		}
	}
	if err == nil {
		if maintenanceErr := m.maybeOptimize(ctx, control, generation, run, false); maintenanceErr != nil {
			err, status, errText = maintenanceErr, "failed", maintenanceErr.Error()
		}
	}
	if err == nil {
		if stats, statsErr := m.lance.Stats(ctx, generation.ID); statsErr == nil {
			files, chunks, _ := generationControlCounts(ctx, control, generation.ID)
			_ = control.UpdateGenerationStats(ctx, generation.ID, files, chunks, stats.TableVersion)
		}
		_ = copyActiveGenerationMeta(ctx, control, cfg, generation)
	}
	_ = control.FinishRun(ctx, run, status, errText)
	return resultFromRun(cfg.AgentKey, run, status, errText), err
}

func (m *Manager) recoverFileOperations(ctx context.Context, control *ControlStore, generationID string) error {
	operations, err := control.PendingFileOperations(ctx, generationID)
	if err != nil {
		return err
	}
	var validation *GenerationValidation
	for _, operation := range operations {
		if operation.RetryCount >= 3 || strings.TrimSpace(operation.DesiredRecordJSON) == "" {
			continue
		}
		if validation == nil {
			value, validationErr := m.lance.Validate(ctx, generationID)
			if validationErr != nil {
				return validationErr
			}
			validation = &value
		}
		if operation.State == FileOperationLanceCommitted && validation.TableVersion < operation.TableVersion {
			continue
		}
		actualHash, fileExists := validation.FileChunkHashes[operation.FileID]
		desiredVisible := operation.Operation == FileOperationDelete && !fileExists ||
			operation.Operation == FileOperationReplace && fileExists && actualHash == operation.DesiredContentHash
		if !desiredVisible {
			continue
		}
		var record fileRecord
		if err := json.Unmarshal([]byte(operation.DesiredRecordJSON), &record); err != nil {
			_ = control.failFileOperation(ctx, operation.ID, err)
			continue
		}
		if record.ID != operation.FileID || normalizeIndexedPath(record.Path) != normalizeIndexedPath(operation.Path) {
			err := fmt.Errorf("file operation record identity mismatch")
			_ = control.failFileOperation(ctx, operation.ID, err)
			continue
		}
		if operation.State == FileOperationPrepared {
			if err := control.MarkFileOperationLanceCommitted(ctx, operation.ID, validation.TableVersion); err != nil {
				_ = control.failFileOperation(ctx, operation.ID, err)
				return err
			}
		}
		if err := control.completeFileOperationWithRecord(ctx, operation.ID, generationID, record); err != nil {
			_ = control.failFileOperation(ctx, operation.ID, err)
			return err
		}
	}
	return nil
}

func (m *Manager) buildLanceGeneration(ctx context.Context, control *ControlStore, cfg resolvedConfig, embedder *Embedder, options RefreshOptions) (RefreshResult, error) {
	generation := newGeneration(cfg)
	if err := control.CreateGeneration(ctx, generation); err != nil {
		return failedRefresh(cfg.AgentKey, options.Mode, err), err
	}
	if err := m.registerLanceGeneration(ctx, cfg, &generation); err != nil {
		_ = control.SetGenerationState(ctx, generation.ID, GenerationFailed, err.Error())
		return failedRefresh(cfg.AgentKey, options.Mode, err), err
	}
	run, err := control.BeginRun(ctx, firstNonBlank(options.Mode, "rebuild"), generation.ID)
	if err != nil {
		return failedRefresh(cfg.AgentKey, options.Mode, err), err
	}
	indexStore := newLanceIndexStore(ctx, control, m.lance, generation.ID)
	if err = indexWorkspace(ctx, indexStore, cfg, embedder, false, &run); err == nil {
		err = m.finishGeneration(ctx, control, &generation, cfg, &run, true, true)
	}
	status, errText := "success", ""
	if err != nil {
		status, errText = "failed", err.Error()
		_ = control.SetGenerationState(ctx, generation.ID, GenerationFailed, errText)
	}
	_ = control.FinishRun(ctx, run, status, errText)
	return resultFromRun(cfg.AgentKey, run, status, errText), err
}

func (m *Manager) finishGeneration(ctx context.Context, control *ControlStore, generation *Generation, cfg resolvedConfig, run *IndexRun, forceOptimize, activate bool) error {
	if err := control.SetGenerationState(ctx, generation.ID, GenerationIndexing, ""); err != nil {
		return err
	}
	indexStarted := time.Now()
	if err := m.lance.BuildIndexes(ctx, generation.ID, m.indexSpec()); err != nil {
		return err
	}
	if err := m.lance.WaitForIndexes(ctx, generation.ID, 30*time.Minute); err != nil {
		return err
	}
	if run != nil {
		run.IndexBuildDurationMS = time.Since(indexStarted).Milliseconds()
	}
	if err := control.SetGenerationState(ctx, generation.ID, GenerationValidating, ""); err != nil {
		return err
	}
	validationStarted := time.Now()
	validation, err := m.lance.Validate(ctx, generation.ID)
	if err != nil {
		return err
	}
	if run != nil {
		run.ValidationDurationMS = time.Since(validationStarted).Milliseconds()
	}
	files, chunks, err := generationControlCounts(ctx, control, generation.ID)
	if err != nil {
		return err
	}
	if !validation.Ready || validation.Chunks != chunks || validation.DuplicateIDs != 0 || validation.InvalidVectors != 0 || !validation.IndexReady {
		return fmt.Errorf("lance generation validation failed: ready=%t chunks=%d/%d duplicateIds=%d invalidVectors=%d indexReady=%t",
			validation.Ready, validation.Chunks, chunks, validation.DuplicateIDs, validation.InvalidVectors, validation.IndexReady)
	}
	if expectedFileDigest, digestErr := controlFileIDDigest(ctx, control, generation.ID); digestErr != nil {
		return digestErr
	} else if validation.FileIDDigest == "" || validation.FileIDDigest != expectedFileDigest {
		return fmt.Errorf("lance generation file ID-set validation failed")
	}
	expectedChunkHashes, err := controlFileChunkHashes(ctx, control, generation.ID)
	if err != nil {
		return err
	}
	if len(validation.FileChunkHashes) != len(expectedChunkHashes) {
		return fmt.Errorf("lance generation per-file chunk validation count failed: got=%d want=%d",
			len(validation.FileChunkHashes), len(expectedChunkHashes))
	}
	for fileID, expected := range expectedChunkHashes {
		if expected == "" || validation.FileChunkHashes[fileID] != expected {
			return fmt.Errorf("lance generation chunk/content/locator validation failed for file %s: got=%s want=%s",
				fileID, validation.FileChunkHashes[fileID], expected)
		}
	}
	stats, err := m.lance.Stats(ctx, generation.ID)
	if err != nil {
		return err
	}
	if err := control.UpdateGenerationStats(ctx, generation.ID, files, chunks, stats.TableVersion); err != nil {
		return err
	}
	generation.Files = files
	generation.Chunks = chunks
	generation.TableVersion = stats.TableVersion
	if forceOptimize {
		if err := m.lance.Optimize(ctx, generation.ID, OptimizeSpec{VersionRetention: m.versionRetention()}); err != nil {
			return err
		}
		optimizedStats, statsErr := m.lance.Stats(ctx, generation.ID)
		if statsErr != nil {
			return statsErr
		}
		generation.TableVersion = optimizedStats.TableVersion
		if err := control.UpdateGenerationStats(ctx, generation.ID, files, chunks, optimizedStats.TableVersion); err != nil {
			return err
		}
		if err := control.SetMeta(ctx, "lastOptimizedAt", strconv.FormatInt(time.Now().UnixMilli(), 10)); err != nil {
			return err
		}
		if err := control.SetMeta(ctx, "generation:"+generation.ID+":changesSinceOptimize", "0"); err != nil {
			return err
		}
	}
	if err := control.SetGenerationState(ctx, generation.ID, GenerationReady, ""); err != nil {
		return err
	}
	if !activate {
		return nil
	}
	return activateGeneration(ctx, control, cfg, generation)
}

func (m *Manager) registerLanceGeneration(ctx context.Context, cfg resolvedConfig, generation *Generation) error {
	if m == nil || m.lance == nil || generation == nil {
		return fmt.Errorf("kbase lance engine is not configured")
	}
	if err := m.lance.CreateGeneration(ctx, GenerationSpec{
		AgentKey:           generation.AgentKey,
		GenerationID:       generation.ID,
		StorageDir:         cfg.StorageDir,
		EmbeddingModel:     generation.EmbeddingModel,
		EmbeddingDimension: generation.EmbeddingDimension,
		FTSBaseTokenizer:   firstNonBlank(generation.FTSTokenizer, "icu"),
	}); err != nil {
		return err
	}
	return writeGenerationManifest(cfg.StorageDir, generation)
}

func writeGenerationManifest(storageDir string, generation *Generation) error {
	if generation == nil {
		return fmt.Errorf("cannot write a nil KBASE generation manifest")
	}
	payload, err := json.MarshalIndent(generation, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	directory := filepath.Join(storageDir, "generations", generation.ID)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, "manifest.json"), payload, 0o644)
}

func (m *Manager) indexSpec() IndexSpec {
	annMinRows := m.options.Index.ANNMinRows
	if annMinRows <= 0 {
		annMinRows = 50000
	}
	return IndexSpec{FTSBaseTokenizer: firstNonBlank(m.options.Index.FTSBaseTokenizer, "icu"), ANNMinRows: annMinRows, Distance: "cosine"}
}

func (m *Manager) maybeOptimize(ctx context.Context, control *ControlStore, generation *Generation, run IndexRun, force bool) error {
	key := "generation:" + generation.ID + ":changesSinceOptimize"
	currentText, _ := control.Meta(ctx, key)
	current, _ := strconv.Atoi(currentText)
	current += run.IndexedChunks + run.DeletedFiles
	lastText, _ := control.Meta(ctx, "lastOptimizedAt")
	last, _ := strconv.ParseInt(lastText, 10, 64)
	threshold := m.options.Maintenance.OptimizeChangeThreshold
	if threshold <= 0 {
		threshold = 1000
	}
	interval := m.options.Maintenance.OptimizeInterval
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	due := last == 0 || time.Since(time.UnixMilli(last)) >= interval
	annDue := false
	if stats, err := m.lance.Stats(ctx, generation.ID); err == nil && stats.Chunks > 0 {
		annDue = stats.UnindexedRows*10 > stats.Chunks
	}
	if !force && current < threshold && !due && !annDue {
		return control.SetMeta(ctx, key, strconv.Itoa(current))
	}
	if err := m.lance.Optimize(ctx, generation.ID, OptimizeSpec{VersionRetention: m.versionRetention()}); err != nil {
		return err
	}
	now := strconv.FormatInt(time.Now().UnixMilli(), 10)
	if err := control.SetMeta(ctx, key, "0"); err != nil {
		return err
	}
	return control.SetMeta(ctx, "lastOptimizedAt", now)
}

func (m *Manager) versionRetention() time.Duration {
	retention := m.options.Maintenance.VersionRetention
	if retention <= 0 {
		retention = 7 * 24 * time.Hour
	}
	return retention
}

func newGeneration(cfg resolvedConfig) Generation {
	return Generation{
		ID:                   fmt.Sprintf("kbg_%d", time.Now().UnixNano()),
		AgentKey:             cfg.AgentKey,
		State:                GenerationBuilding,
		WorkspaceRoot:        cfg.WorkspaceRoot,
		StorageDir:           cfg.StorageDir,
		EmbeddingModelKey:    cfg.Embedding.ModelKey,
		EmbeddingProviderKey: cfg.Embedding.ProviderKey,
		EmbeddingModel:       cfg.Embedding.Model,
		EmbeddingDimension:   cfg.Embedding.Dimension,
		FTSTokenizer:         firstNonBlank(cfg.FTSTokenizer, "icu"),
		IndexHash:            desiredIndexHash(cfg),
		CreatedAt:            time.Now().UnixMilli(),
	}
}

func generationControlCounts(ctx context.Context, control *ControlStore, generationID string) (files, chunks int, err error) {
	records, err := control.Files(ctx, generationID)
	if err != nil {
		return 0, 0, err
	}
	for _, record := range records {
		if record.Status == "active" {
			files++
			chunks += record.ChunkCount
		}
	}
	return files, chunks, nil
}

func controlFileIDDigest(ctx context.Context, control *ControlStore, generationID string) (string, error) {
	records, err := control.Files(ctx, generationID)
	if err != nil {
		return "", err
	}
	ids := make([]string, 0, len(records))
	for _, record := range records {
		if record.Status == "active" && record.ChunkCount > 0 {
			ids = append(ids, record.ID)
		}
	}
	return stableIDDigest(ids), nil
}

func controlFileChunkHashes(ctx context.Context, control *ControlStore, generationID string) (map[string]string, error) {
	records, err := control.Files(ctx, generationID)
	if err != nil {
		return nil, err
	}
	hashes := map[string]string{}
	for _, record := range records {
		if record.Status == "active" && record.ChunkCount > 0 {
			hashes[record.ID] = record.ChunkSetHash
		}
	}
	return hashes, nil
}

func activeGenerationMeta(cfg resolvedConfig, generation *Generation) map[string]string {
	values := map[string]string{
		"schemaVersion":        ControlSchemaVersion,
		"engine":               "lancedb",
		"indexHash":            generation.IndexHash,
		"queryHash":            desiredQueryHash(cfg),
		"configHash":           generation.IndexHash,
		"embeddingModelKey":    generation.EmbeddingModelKey,
		"embeddingProviderKey": generation.EmbeddingProviderKey,
		"embeddingModel":       generation.EmbeddingModel,
		"embeddingDimension":   strconv.Itoa(generation.EmbeddingDimension),
		"ftsTokenizer":         firstNonBlank(generation.FTSTokenizer, "icu"),
		"lastIndexedAt":        strconv.FormatInt(time.Now().UnixMilli(), 10),
	}
	return values
}

func activateGeneration(ctx context.Context, control *ControlStore, cfg resolvedConfig, generation *Generation) error {
	if generation == nil {
		return fmt.Errorf("cannot activate a nil KBASE generation")
	}
	if err := control.ActivateGenerationWithMeta(ctx, generation.ID, activeGenerationMeta(cfg, generation)); err != nil {
		return err
	}
	generation.State = GenerationActive
	generation.ActivatedAt = time.Now().UnixMilli()
	if err := writeGenerationManifest(cfg.StorageDir, generation); err != nil {
		log.Printf("[kbase-lance] generation manifest update failed agent=%s generation=%s: %v", generation.AgentKey, generation.ID, err)
	}
	return nil
}

func copyActiveGenerationMeta(ctx context.Context, control *ControlStore, cfg resolvedConfig, generation *Generation) error {
	if generation == nil {
		return fmt.Errorf("cannot publish metadata for a nil KBASE generation")
	}
	values := activeGenerationMeta(cfg, generation)
	if err := control.SetMeta(ctx, "activeGeneration", generation.ID); err != nil {
		return err
	}
	for key, value := range values {
		if err := control.SetMeta(ctx, key, value); err != nil {
			return err
		}
	}
	return nil
}

func resultFromRun(agentKey string, run IndexRun, status, errText string) RefreshResult {
	return RefreshResult{AgentKey: agentKey, Mode: run.Mode, Status: status, ScannedFiles: run.ScannedFiles,
		ChangedFiles: run.ChangedFiles, DeletedFiles: run.DeletedFiles, IndexedChunks: run.IndexedChunks, Error: errText}
}

func failedRefresh(agentKey, mode string, err error) RefreshResult {
	return RefreshResult{AgentKey: agentKey, Mode: firstNonBlank(mode, "manual"), Status: "failed", Error: err.Error()}
}

func (m *Manager) selectRetrieval(ctx context.Context, cfg resolvedConfig) (RetrievalStore, string, bool, error) {
	control, err := OpenReadControlStore(cfg.StorageDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", false, nil
		}
		return nil, "", false, err
	}
	active, err := control.ActiveGeneration(ctx)
	_ = control.Close()
	if err != nil {
		return nil, "", false, err
	}
	if active == nil {
		return nil, "", false, nil
	}
	if err := m.registerLanceGeneration(ctx, cfg, active); err != nil {
		return nil, "", false, &PolicyError{Kind: ErrorUnavailable, Message: "KBASE LanceDB sidecar is unavailable: " + err.Error()}
	}
	return m.lance, active.ID, true, nil
}

func (m *Manager) releaseStorageGeneration(agentKey, storageDir string) {
	if m == nil || m.lance == nil || strings.TrimSpace(storageDir) == "" || !m.engine.State().Available {
		return
	}
	control, err := OpenReadControlStore(storageDir)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	generation, err := control.ActiveGeneration(ctx)
	_ = control.Close()
	if err != nil || generation == nil {
		return
	}
	if _, err := m.lance.ReleaseGeneration(ctx, generation.ID, agentKey); err != nil {
		log.Printf("[kbase-lance] release generation failed agent=%s generation=%s: %v", agentKey, generation.ID, err)
	}
}

// RollbackGeneration atomically reactivates a retained ready/retired
// generation. An empty generationID selects the most recently active prior
// generation. It never copies or renames Lance data.
func (m *Manager) RollbackGeneration(ctx context.Context, agentKey, generationID string) (*Generation, error) {
	cfg, _, err := m.resolve(agentKey)
	if err != nil {
		return nil, err
	}
	lock := m.storageLock(storageLockKey(cfg.StorageDir))
	lock.Lock()
	defer lock.Unlock()
	control, err := OpenControlStore(cfg.StorageDir)
	if err != nil {
		return nil, err
	}
	defer control.Close()
	active, err := control.ActiveGeneration(ctx)
	if err != nil || active == nil {
		return nil, firstNonNil(err, fmt.Errorf("no active KBASE generation"))
	}
	var target *Generation
	if strings.TrimSpace(generationID) == "" {
		target, err = control.PreviousGeneration(ctx, active.ID)
	} else {
		target, err = control.Generation(ctx, strings.TrimSpace(generationID))
	}
	if err != nil {
		return nil, err
	}
	if target == nil || target.ID == active.ID || target.AgentKey != cfg.AgentKey ||
		(target.State != GenerationReady && target.State != GenerationRetired) {
		return nil, &PolicyError{Kind: ErrorInvalid, Message: "requested KBASE rollback generation is not ready or retained"}
	}
	if err := m.registerLanceGeneration(ctx, cfg, target); err != nil {
		return nil, err
	}
	validation, err := m.lance.Validate(ctx, target.ID)
	if err != nil || !validation.Ready {
		if err == nil {
			err = fmt.Errorf("rollback generation validation failed")
		}
		return nil, err
	}
	if err := activateGeneration(ctx, control, cfg, target); err != nil {
		return nil, err
	}
	target.State = GenerationActive
	return target, nil
}

func firstNonNil(err error, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

func readResultFromChunks(path string, offset int, chunks []chunkRecord) ReadResult {
	if len(chunks) == 0 {
		return ReadResult{Found: false, Path: path}
	}
	if offset <= 0 {
		offset = 1
	}
	parts := make([]string, 0, len(chunks))
	result := ReadResult{Found: true, Path: path}
	for index, chunk := range chunks {
		if index == 0 {
			result.StartLine = maxInt(chunk.StartLine, offset)
			result.PageStart = chunk.PageStart
			result.SlideStart = chunk.SlideStart
			result.SourceType = chunk.SourceType
		}
		result.EndLine = chunk.EndLine
		if chunk.PageEnd > 0 {
			result.PageEnd = chunk.PageEnd
		}
		if chunk.SlideEnd > 0 {
			result.SlideEnd = chunk.SlideEnd
		}
		parts = append(parts, chunk.Content)
	}
	result.Content = strings.Join(parts, "\n")
	return result
}

func storageDiskUsage(root string) int64 {
	var size int64
	_ = filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		if info, infoErr := entry.Info(); infoErr == nil {
			size += info.Size()
		}
		return nil
	})
	return size
}

func float32Vector(vector []float64, expected int) ([]float32, error) {
	if len(vector) != expected {
		return nil, fmt.Errorf("query embedding dimension mismatch: got %d want %d", len(vector), expected)
	}
	out := make([]float32, len(vector))
	for index, value := range vector {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, fmt.Errorf("query embedding contains NaN or Inf")
		}
		out[index] = float32(value)
	}
	return out, nil
}
