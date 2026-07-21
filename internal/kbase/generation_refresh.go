package kbase

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func (s *generationService) Refresh(ctx context.Context, cfg resolvedConfig, embedder *Embedder, options RefreshOptions, pendingChanges func() int) (RefreshResult, error) {
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
			options.Scope = "rebuild"
			return s.buildGeneration(ctx, control, cfg, embedder, options, pendingChanges)
		}
		return s.refreshGeneration(ctx, control, active, cfg, embedder, options, pendingChanges)
	}
	options.Scope = "rebuild"
	return s.buildGeneration(ctx, control, cfg, embedder, options, pendingChanges)
}

func (s *generationService) refreshGeneration(ctx context.Context, control *ControlStore, generation *Generation, cfg resolvedConfig, embedder *Embedder, options RefreshOptions, pendingChanges func() int) (RefreshResult, error) {
	if err := s.RegisterGeneration(ctx, cfg, generation); err != nil {
		return failedRefresh(cfg.AgentKey, options.Mode, err), err
	}
	if err := s.recoverFileOperations(ctx, control, generation.ID); err != nil {
		return failedRefresh(cfg.AgentKey, options.Mode, err), err
	}
	if err := control.preparePendingRecovery(ctx, generation.ID); err != nil {
		return failedRefresh(cfg.AgentKey, options.Mode, err), err
	}
	run, err := control.BeginRun(ctx, firstNonBlank(options.Mode, "manual"), generation.ID)
	if err != nil {
		return failedRefresh(cfg.AgentKey, options.Mode, err), err
	}
	run.Scope = firstNonBlank(options.Scope, refreshScope(options))
	indexStore := newLanceIndexStore(ctx, control, s.runtime.store, generation.ID)
	if len(options.Paths) > 0 && run.Scope == "delta" {
		err = indexWorkspacePaths(ctx, indexStore, cfg, embedder, options.Paths, &run)
	} else {
		err = indexWorkspace(ctx, indexStore, cfg, embedder, false, &run)
	}
	status, errText := "success", ""
	if err != nil {
		status, errText = "failed", err.Error()
	}
	if err == nil {
		if maintenanceErr := s.maybeMaintainIndexes(ctx, control, generation, run); maintenanceErr != nil {
			err, status, errText = maintenanceErr, "failed", maintenanceErr.Error()
		}
	}
	if err == nil {
		if stats, statsErr := s.runtime.store.Stats(ctx, generation.ID); statsErr == nil {
			files, chunks, _ := generationControlCounts(ctx, control, generation.ID)
			_ = control.UpdateGenerationStats(ctx, generation.ID, files, chunks, stats.TableVersion)
		}
		_ = copyActiveGenerationMeta(ctx, control, cfg, generation)
		_, _ = control.PurgeDeletedBefore(ctx, generation.ID, time.Now().Add(-s.versionRetention()).UnixMilli())
	}
	run.PendingChanges = pendingChangeCount(pendingChanges)
	_ = control.FinishRun(ctx, run, status, errText)
	return resultFromRun(cfg.AgentKey, run, status, errText), err
}

func (s *generationService) recoverFileOperations(ctx context.Context, control *ControlStore, generationID string) error {
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
			value, validationErr := s.runtime.store.Validate(ctx, generationID)
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

func (s *generationService) buildGeneration(ctx context.Context, control *ControlStore, cfg resolvedConfig, embedder *Embedder, options RefreshOptions, pendingChanges func() int) (RefreshResult, error) {
	generation := newGeneration(cfg)
	if err := control.CreateGeneration(ctx, generation); err != nil {
		return failedRefresh(cfg.AgentKey, options.Mode, err), err
	}
	if err := s.RegisterGeneration(ctx, cfg, &generation); err != nil {
		_ = control.SetGenerationState(ctx, generation.ID, GenerationFailed, err.Error())
		return failedRefresh(cfg.AgentKey, options.Mode, err), err
	}
	run, err := control.BeginRun(ctx, firstNonBlank(options.Mode, "rebuild"), generation.ID)
	if err != nil {
		return failedRefresh(cfg.AgentKey, options.Mode, err), err
	}
	run.Scope = "rebuild"
	indexStore := newLanceIndexStore(ctx, control, s.runtime.store, generation.ID)
	if err = indexWorkspace(ctx, indexStore, cfg, embedder, false, &run); err == nil {
		err = s.finishGeneration(ctx, control, &generation, cfg, &run, true, true)
	}
	status, errText := "success", ""
	if err != nil {
		status, errText = "failed", err.Error()
		_ = control.SetGenerationState(ctx, generation.ID, GenerationFailed, errText)
	}
	run.PendingChanges = pendingChangeCount(pendingChanges)
	_ = control.FinishRun(ctx, run, status, errText)
	return resultFromRun(cfg.AgentKey, run, status, errText), err
}

func (s *generationService) finishGeneration(ctx context.Context, control *ControlStore, generation *Generation, cfg resolvedConfig, run *IndexRun, forceOptimize, activate bool) error {
	if err := control.SetGenerationState(ctx, generation.ID, GenerationIndexing, ""); err != nil {
		return err
	}
	indexStarted := time.Now()
	if err := s.runtime.store.BuildIndexes(ctx, generation.ID, s.indexSpec()); err != nil {
		return err
	}
	if err := s.runtime.store.WaitForIndexes(ctx, generation.ID, 30*time.Minute); err != nil {
		return err
	}
	if run != nil {
		run.IndexBuildDurationMS = time.Since(indexStarted).Milliseconds()
	}
	if err := control.SetGenerationState(ctx, generation.ID, GenerationValidating, ""); err != nil {
		return err
	}
	validationStarted := time.Now()
	validation, err := s.runtime.store.Validate(ctx, generation.ID)
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
		return fmt.Errorf("lance generation per-file chunk validation count failed: got=%d want=%d", len(validation.FileChunkHashes), len(expectedChunkHashes))
	}
	for fileID, expected := range expectedChunkHashes {
		if expected == "" || validation.FileChunkHashes[fileID] != expected {
			return fmt.Errorf("lance generation chunk/content/locator validation failed for file %s: got=%s want=%s", fileID, validation.FileChunkHashes[fileID], expected)
		}
	}
	stats, err := s.runtime.store.Stats(ctx, generation.ID)
	if err != nil {
		return err
	}
	if err := control.UpdateGenerationStats(ctx, generation.ID, files, chunks, stats.TableVersion); err != nil {
		return err
	}
	generation.Files, generation.Chunks, generation.TableVersion = files, chunks, stats.TableVersion
	if forceOptimize {
		if err := s.runtime.store.Optimize(ctx, generation.ID, OptimizeSpec{VersionRetention: s.versionRetention()}); err != nil {
			return err
		}
		optimizedStats, statsErr := s.runtime.store.Stats(ctx, generation.ID)
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
		if err := control.SetMeta(ctx, "generation:"+generation.ID+":changesSinceIndexRefresh", "0"); err != nil {
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

func (s *generationService) indexSpec() IndexSpec {
	annMinRows := s.index.ANNMinRows
	if annMinRows <= 0 {
		annMinRows = 50000
	}
	return IndexSpec{FTSBaseTokenizer: firstNonBlank(s.index.FTSBaseTokenizer, "icu"), ANNMinRows: annMinRows, Distance: "cosine"}
}

func (s *generationService) maybeMaintainIndexes(ctx context.Context, control *ControlStore, generation *Generation, run IndexRun) error {
	key := "generation:" + generation.ID + ":changesSinceIndexRefresh"
	currentText, _ := control.Meta(ctx, key)
	current, _ := strconv.Atoi(currentText)
	current += run.IndexedChunks
	if err := control.SetMeta(ctx, key, strconv.Itoa(current)); err != nil {
		return err
	}
	threshold := s.maintenance.OptimizeChangeThreshold
	if threshold <= 0 {
		threshold = 1000
	}
	stats, statsErr := s.runtime.store.Stats(ctx, generation.ID)
	if statsErr != nil {
		return statsErr
	}
	unindexed := maxInt(stats.UnindexedRows, stats.FTSUnindexedRows)
	dueByRatio := stats.Chunks > 0 && unindexed*10 > stats.Chunks
	if unindexed > 0 && (current >= threshold || dueByRatio) {
		if err := s.runtime.store.RefreshIndexes(ctx, generation.ID); err != nil {
			return err
		}
		if err := control.SetMeta(ctx, key, "0"); err != nil {
			return err
		}
		if err := control.SetMeta(ctx, "lastIndexRefreshedAt", strconv.FormatInt(time.Now().UnixMilli(), 10)); err != nil {
			return err
		}
	}
	lastText, _ := control.Meta(ctx, "lastOptimizedAt")
	last, _ := strconv.ParseInt(lastText, 10, 64)
	interval := s.maintenance.OptimizeInterval
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	due := last == 0 || time.Since(time.UnixMilli(last)) >= interval
	if !due || run.Scope == "delta" {
		return nil
	}
	if err := s.runtime.store.Optimize(ctx, generation.ID, OptimizeSpec{VersionRetention: s.versionRetention()}); err != nil {
		return err
	}
	now := strconv.FormatInt(time.Now().UnixMilli(), 10)
	if err := control.SetMeta(ctx, key, "0"); err != nil {
		return err
	}
	return control.SetMeta(ctx, "lastOptimizedAt", now)
}

func (s *generationService) versionRetention() time.Duration {
	retention := s.maintenance.VersionRetention
	if retention <= 0 {
		retention = 7 * 24 * time.Hour
	}
	return retention
}

func refreshScope(options RefreshOptions) string {
	if options.Force {
		return "rebuild"
	}
	if len(options.Paths) > 0 {
		return "delta"
	}
	return "reconcile"
}

func pendingChangeCount(value func() int) int {
	if value == nil {
		return 0
	}
	return value()
}

func newGeneration(cfg resolvedConfig) Generation {
	return Generation{
		ID: fmt.Sprintf("kbg_%d", time.Now().UnixNano()), AgentKey: cfg.AgentKey, State: GenerationBuilding,
		WorkspaceRoot: cfg.WorkspaceRoot, StorageDir: cfg.StorageDir,
		EmbeddingModelKey: cfg.Embedding.ModelKey, EmbeddingProviderKey: cfg.Embedding.ProviderKey,
		EmbeddingModel: cfg.Embedding.Model, EmbeddingDimension: cfg.Embedding.Dimension,
		FTSTokenizer: firstNonBlank(cfg.FTSTokenizer, "icu"), IndexHash: desiredIndexHash(cfg), CreatedAt: time.Now().UnixMilli(),
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

func resultFromRun(agentKey string, run IndexRun, status, errText string) RefreshResult {
	return RefreshResult{AgentKey: agentKey, Mode: run.Mode, Status: status, Scope: run.Scope,
		CandidatePaths: run.CandidatePaths, ScannedFiles: run.ScannedFiles, ChangedFiles: run.ChangedFiles,
		NewFiles: run.NewFiles, ModifiedFiles: run.ModifiedFiles, MetadataOnlyFiles: run.MetadataOnlyFiles,
		UnchangedFiles: run.UnchangedFiles, DeletedFiles: run.DeletedFiles, IndexedChunks: run.IndexedChunks,
		EmbeddedChunks: run.EmbeddedChunks, ReusedChunks: run.ReusedChunks, PendingChanges: run.PendingChanges, Error: errText}
}

func failedRefresh(agentKey, mode string, err error) RefreshResult {
	return RefreshResult{AgentKey: agentKey, Mode: firstNonBlank(mode, "manual"), Status: "failed", Error: err.Error()}
}
