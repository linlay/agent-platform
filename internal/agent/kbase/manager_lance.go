package kbase

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (m *Manager) engineMode() string {
	mode := strings.ToLower(strings.TrimSpace(m.options.StorageEngine))
	switch mode {
	case "auto", "lancedb", "sqlite":
		return mode
	default:
		// Direct package users and legacy tests did not configure an engine.
		// Keep those callers on SQLite; the application adapter explicitly
		// supplies the product default (auto).
		return "sqlite"
	}
}

func (m *Manager) refreshResolved(ctx context.Context, cfg resolvedConfig, embedder *Embedder, options RefreshOptions) (RefreshResult, error) {
	mode := m.engineMode()
	if mode == "sqlite" {
		return refreshSQLite(ctx, cfg, embedder, options)
	}
	lanceCfg := resolvedLanceConfig(cfg)

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
		if options.Force || active.IndexHash != desiredIndexHash(lanceCfg) || active.EmbeddingDimension != cfg.Embedding.Dimension || active.EmbeddingModel != cfg.Embedding.Model {
			return m.buildLanceGeneration(ctx, control, lanceCfg, embedder, options)
		}
		return m.refreshLanceGeneration(ctx, control, active, lanceCfg, embedder, options)
	}

	legacyExists := fileExists(filepath.Join(cfg.StorageDir, "kbase.db"))
	if mode == "auto" {
		legacyResult, legacyErr := refreshSQLite(ctx, cfg, embedder, options)
		if legacyErr != nil {
			return legacyResult, legacyErr
		}
		if !m.options.Migration.Enabled {
			return legacyResult, nil
		}
		if _, err := m.migrateLegacy(ctx, control, lanceCfg, embedder, options); err != nil {
			if errors.Is(err, errLegacyRequiresRebuild) {
				if rebuilt, rebuildErr := m.buildLanceGeneration(ctx, control, lanceCfg, embedder, options); rebuildErr == nil {
					return rebuilt, nil
				} else {
					log.Printf("[kbase-lance] cold rebuild failed agent=%s: %v", cfg.AgentKey, rebuildErr)
				}
			}
			log.Printf("[kbase-lance] background migration failed agent=%s: %v", cfg.AgentKey, err)
			// Auto mode keeps the freshly-updated legacy engine available.
			return legacyResult, nil
		}
		return legacyResult, nil
	}

	if legacyExists && m.options.Migration.Enabled {
		result, err := m.migrateLegacy(ctx, control, lanceCfg, embedder, options)
		if err == nil {
			return result, nil
		}
		if !errors.Is(err, errLegacyRequiresRebuild) {
			return failedRefresh(cfg.AgentKey, options.Mode, err), err
		}
	}
	return m.buildLanceGeneration(ctx, control, lanceCfg, embedder, options)
}

func refreshSQLite(ctx context.Context, cfg resolvedConfig, embedder *Embedder, options RefreshOptions) (RefreshResult, error) {
	store, err := OpenStore(cfg.StorageDir)
	if err != nil {
		return failedRefresh(cfg.AgentKey, options.Mode, err), err
	}
	defer store.Close()
	run, err := store.BeginRun(firstNonBlank(options.Mode, "manual"))
	if err != nil {
		return failedRefresh(cfg.AgentKey, options.Mode, err), err
	}
	status := "success"
	errText := ""
	if err = indexWorkspace(ctx, store, cfg, embedder, options.Force, &run); err != nil {
		status = "failed"
		errText = err.Error()
	}
	_ = store.FinishRun(run, status, errText)
	if err == nil && fileExists(filepath.Join(cfg.StorageDir, "control.db")) {
		if control, controlErr := OpenControlStore(cfg.StorageDir); controlErr == nil {
			_ = control.SetMeta(ctx, "legacyNeedsRefresh", "false")
			_ = control.Close()
		}
	}
	return resultFromRun(cfg.AgentKey, run, status, errText), err
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

func (m *Manager) migrateLegacy(ctx context.Context, control *ControlStore, cfg resolvedConfig, embedder *Embedder, options RefreshOptions) (RefreshResult, error) {
	select {
	case m.migrationSem <- struct{}{}:
		defer func() { <-m.migrationSem }()
	case <-ctx.Done():
		return failedRefresh(cfg.AgentKey, options.Mode, ctx.Err()), ctx.Err()
	}
	legacy, err := OpenStore(cfg.StorageDir)
	if err != nil {
		return failedRefresh(cfg.AgentKey, options.Mode, err), err
	}
	defer legacy.Close()
	generation := newGeneration(cfg)
	migration := Migration{}
	resuming := false
	if latest, latestErr := control.LatestMigration(ctx, cfg.AgentKey); latestErr == nil && latest != nil &&
		migrationImportCanResume(latest) && latest.ErrorCode != "legacy_requires_rebuild" {
		if previous, generationErr := control.Generation(ctx, latest.GenerationID); generationErr == nil && previous != nil &&
			previous.EmbeddingDimension == cfg.Embedding.Dimension && previous.EmbeddingModel == cfg.Embedding.Model &&
			previous.IndexHash == desiredIndexHash(cfg) && fileExists(filepath.Join(cfg.StorageDir, "migrations", latest.ID+".snapshot.db")) {
			generation = *previous
			migration = *latest
			migration.State = MigrationImporting
			migration.ImportedFiles = 0
			migration.ImportedChunks = 0
			migration.Progress = 0
			migration.FinishedAt = 0
			migration.ErrorCode = ""
			migration.Error = ""
			migration.RetryCount++
			resuming = true
			_ = control.SetGenerationState(ctx, generation.ID, GenerationBuilding, "")
			_ = control.UpdateMigration(ctx, migration)
		}
	}
	if !resuming {
		migration = Migration{
			ID:           fmt.Sprintf("kbm_%d", time.Now().UnixNano()),
			AgentKey:     cfg.AgentKey,
			SourceEngine: "sqlite",
			SourceSchema: legacy.Meta("schemaVersion"),
			GenerationID: generation.ID,
			State:        MigrationSnapshotting,
			StartedAt:    time.Now().UnixMilli(),
		}
		if err := control.BeginMigration(ctx, migration); err != nil {
			return failedRefresh(cfg.AgentKey, options.Mode, err), err
		}
		if err := control.CreateGeneration(ctx, generation); err != nil {
			return m.failMigration(ctx, control, migration, generation, err)
		}
	}

	legacyDimension, _ := strconv.Atoi(legacy.Meta("embeddingDimension"))
	legacyModel := strings.TrimSpace(legacy.Meta("embeddingModel"))
	if legacyDimension != cfg.Embedding.Dimension || legacyModel != "" && legacyModel != cfg.Embedding.Model {
		err := fmt.Errorf("%w: legacy embedding model/dimension does not match current configuration", errLegacyRequiresRebuild)
		return m.failMigration(ctx, control, migration, generation, err)
	}
	files, chunks, err := legacy.Counts()
	if err != nil {
		return m.failMigration(ctx, control, migration, generation, err)
	}
	migration.TotalFiles, migration.TotalChunks = files, chunks
	_ = control.UpdateMigration(ctx, migration)

	snapshotPath := filepath.Join(cfg.StorageDir, "migrations", migration.ID+".snapshot.db")
	if !resuming {
		if err := legacy.Snapshot(ctx, snapshotPath); err != nil {
			return m.failMigration(ctx, control, migration, generation, err)
		}
	}
	snapshot, err := OpenSnapshotStore(snapshotPath)
	if err != nil {
		return m.failMigration(ctx, control, migration, generation, err)
	}
	defer snapshot.Close()

	if err := m.registerLanceGeneration(ctx, cfg, &generation); err != nil {
		return m.failMigration(ctx, control, migration, generation, err)
	}
	migration.State = MigrationImporting
	_ = control.UpdateMigration(ctx, migration)
	legacyFiles, err := snapshot.AllFiles()
	if err != nil {
		return m.failMigration(ctx, control, migration, generation, err)
	}
	legacyChunkHashes, err := snapshot.ChunkValidationHashes()
	if err != nil {
		return m.failMigration(ctx, control, migration, generation, err)
	}
	for _, file := range legacyFiles {
		if file.Status == "active" && file.ChunkCount > 0 {
			file.ChunkSetHash = legacyChunkHashes[file.ID]
			if file.ChunkSetHash == "" {
				return m.failMigration(ctx, control, migration, generation,
					fmt.Errorf("legacy file %s is missing its chunk validation digest", file.Path))
			}
		}
		if err := control.UpsertFile(ctx, generation.ID, file); err != nil {
			return m.failMigration(ctx, control, migration, generation, err)
		}
		if file.Status == "active" {
			migration.ImportedFiles++
		}
	}
	if err := snapshot.IterateChunks(512, cfg.Embedding.Dimension, func(batch []chunkRecord) error {
		if err := m.lance.ImportChunks(ctx, generation.ID, batch); err != nil {
			return err
		}
		migration.ImportedChunks += len(batch)
		if migration.TotalChunks > 0 {
			migration.Progress = float64(migration.ImportedChunks) / float64(migration.TotalChunks)
		}
		return control.UpdateMigration(ctx, migration)
	}); err != nil {
		if isLegacyDataError(err) {
			err = fmt.Errorf("%w: %v", errLegacyRequiresRebuild, err)
		}
		return m.failMigration(ctx, control, migration, generation, err)
	}
	legacyChunkDigest, legacyFileDigest, err := snapshot.IDDigests()
	if err != nil {
		return m.failMigration(ctx, control, migration, generation, err)
	}
	importValidation, err := m.lance.Validate(ctx, generation.ID)
	if err != nil {
		return m.failMigration(ctx, control, migration, generation, err)
	}
	if importValidation.ChunkIDDigest != legacyChunkDigest || importValidation.FileIDDigest != legacyFileDigest ||
		importValidation.Chunks != migration.TotalChunks {
		err = fmt.Errorf("legacy import ID-set validation failed: chunks=%d/%d chunkDigest=%t fileDigest=%t",
			importValidation.Chunks, migration.TotalChunks, importValidation.ChunkIDDigest == legacyChunkDigest,
			importValidation.FileIDDigest == legacyFileDigest)
		return m.failMigration(ctx, control, migration, generation, err)
	}

	// Catch workspace changes that happened while the consistent snapshot was
	// being imported. Only changed files call the embedding provider.
	run, err := control.BeginRun(ctx, firstNonBlank(options.Mode, "migration"), generation.ID)
	if err != nil {
		return m.failMigration(ctx, control, migration, generation, err)
	}
	indexStore := newLanceIndexStore(ctx, control, m.lance, generation.ID)
	// Once workspace reconciliation starts the generation may contain rows that
	// are intentionally absent from the snapshot. A crash from this point must
	// start a fresh generation instead of replaying an upsert-only snapshot into
	// the changed table and failing its exact ID-set gate forever.
	migration.State = MigrationIndexing
	_ = control.UpdateMigration(ctx, migration)
	if err = indexWorkspace(ctx, indexStore, cfg, embedder, false, &run); err == nil {
		migration.State = MigrationValidating
		_ = control.UpdateMigration(ctx, migration)
		run.MigratedChunks = migration.ImportedChunks
		err = m.finishGeneration(ctx, control, &generation, cfg, &run, false, false)
	}
	status, errText := "success", ""
	if err != nil {
		status, errText = "failed", err.Error()
		_ = control.FinishRun(ctx, run, status, errText)
		return m.failMigration(ctx, control, migration, generation, err)
	}
	migration.State = MigrationShadowing
	_ = control.UpdateMigration(ctx, migration)
	if err = m.validateMigrationRetrieval(ctx, snapshot, generation, cfg); err != nil {
		status, errText = "failed", err.Error()
		_ = control.FinishRun(ctx, run, status, errText)
		return m.failMigration(ctx, control, migration, generation, err)
	}
	migration.State = MigrationReady
	_ = control.UpdateMigration(ctx, migration)
	if err = activateGeneration(ctx, control, cfg, &generation); err != nil {
		status, errText = "failed", err.Error()
		_ = control.FinishRun(ctx, run, status, errText)
		return m.failMigration(ctx, control, migration, generation, err)
	}
	_ = control.FinishRun(ctx, run, status, errText)
	migration.State = MigrationActive
	migration.Progress = 1
	migration.FinishedAt = time.Now().UnixMilli()
	_ = control.UpdateMigration(ctx, migration)
	if removeErr := os.Remove(snapshotPath); removeErr != nil && !os.IsNotExist(removeErr) {
		log.Printf("[kbase-lance] remove migration snapshot failed agent=%s path=%s: %v", cfg.AgentKey, snapshotPath, removeErr)
	}
	return resultFromRun(cfg.AgentKey, run, status, errText), nil
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

type migrationValidationReport struct {
	Queries           int     `json:"queries"`
	NonEmptyQueries   int     `json:"nonEmptyQueries"`
	AverageOverlapAt8 float64 `json:"averageOverlapAt8"`
	OldTop1InNewTop8  float64 `json:"oldTop1InNewTop8"`
	RetrievalP95MS    int64   `json:"retrievalP95Ms"`
	Passed            bool    `json:"passed"`
	Error             string  `json:"error,omitempty"`
}

func (m *Manager) validateMigrationRetrieval(ctx context.Context, legacy *Store, generation Generation, cfg resolvedConfig) error {
	maxQueries := m.options.Migration.MaxReplayQueries
	if maxQueries < 0 {
		maxQueries = 0
	}
	queries, err := legacy.SampleValidationQueries(maxQueries)
	if err != nil {
		return err
	}
	report := migrationValidationReport{Queries: len(queries), Passed: true}
	var overlapSum float64
	var top1Hits int
	var durations []time.Duration
	for _, query := range queries {
		vector, err := float32Vector(query.Vector, cfg.Embedding.Dimension)
		if err != nil {
			return m.writeMigrationValidationReport(cfg.StorageDir, generation.ID, report, err)
		}
		req := RetrievalRequest{Query: query.Text, Vector: vector, Limit: 8, RRFK: cfg.Retrieval.RRFK,
			VectorWeight: cfg.Retrieval.VectorWeight, FTSWeight: cfg.Retrieval.FTSWeight,
			CandidateFloor: cfg.Retrieval.CandidateFloor, CandidateMultiplier: cfg.Retrieval.CandidateMultiplier,
			CandidateMax: cfg.Retrieval.CandidateMax}
		oldResult, err := searchSQLiteStore(legacy, req)
		if err != nil {
			return m.writeMigrationValidationReport(cfg.StorageDir, generation.ID, report, err)
		}
		started := time.Now()
		newResult, err := m.lance.Search(ctx, generation.ID, req)
		durations = append(durations, time.Since(started))
		if err != nil {
			return m.writeMigrationValidationReport(cfg.StorageDir, generation.ID, report, err)
		}
		if len(oldResult.Matches) == 0 {
			continue
		}
		report.NonEmptyQueries++
		if len(newResult.Matches) == 0 {
			return m.writeMigrationValidationReport(cfg.StorageDir, generation.ID, report,
				fmt.Errorf("shadow query produced no Lance results for a non-empty legacy result"))
		}
		newIDs := make(map[string]struct{}, len(newResult.Matches))
		for _, match := range newResult.Matches {
			newIDs[match.Chunk.ID] = struct{}{}
		}
		overlap := 0
		for _, match := range oldResult.Matches {
			if _, ok := newIDs[match.Chunk.ID]; ok {
				overlap++
			}
		}
		overlapSum += float64(overlap) / float64(minInt(8, len(oldResult.Matches)))
		if _, ok := newIDs[oldResult.Matches[0].Chunk.ID]; ok {
			top1Hits++
		}
	}
	if report.NonEmptyQueries > 0 {
		report.AverageOverlapAt8 = overlapSum / float64(report.NonEmptyQueries)
		report.OldTop1InNewTop8 = float64(top1Hits) / float64(report.NonEmptyQueries)
	}
	if len(durations) > 0 {
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		index := int(math.Ceil(float64(len(durations))*0.95)) - 1
		if index < 0 {
			index = 0
		}
		report.RetrievalP95MS = durations[index].Milliseconds()
	}
	if report.NonEmptyQueries > 0 && report.AverageOverlapAt8 < 0.70 {
		err = fmt.Errorf("shadow overlap@8 %.3f is below 0.70", report.AverageOverlapAt8)
	} else if report.NonEmptyQueries > 0 && report.OldTop1InNewTop8 < 0.95 {
		err = fmt.Errorf("shadow old-top1-in-new-top8 %.3f is below 0.95", report.OldTop1InNewTop8)
	} else {
		latencyLimit := int64(500)
		if generation.Chunks <= 10000 {
			latencyLimit = 200
		}
		if report.RetrievalP95MS > latencyLimit {
			err = fmt.Errorf("Lance retrieval p95 %dms exceeds %dms", report.RetrievalP95MS, latencyLimit)
		}
	}
	return m.writeMigrationValidationReport(cfg.StorageDir, generation.ID, report, err)
}

func (m *Manager) writeMigrationValidationReport(storageDir, generationID string, report migrationValidationReport, validationErr error) error {
	if validationErr != nil {
		report.Passed = false
		report.Error = validationErr.Error()
	}
	payload, marshalErr := json.MarshalIndent(report, "", "  ")
	if marshalErr == nil {
		payload = append(payload, '\n')
		path := filepath.Join(storageDir, "generations", generationID, "validation.json")
		if mkdirErr := os.MkdirAll(filepath.Dir(path), 0o755); mkdirErr == nil {
			_ = os.WriteFile(path, payload, 0o644)
		}
	}
	return validationErr
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

func (m *Manager) failMigration(ctx context.Context, control *ControlStore, migration Migration, generation Generation, err error) (RefreshResult, error) {
	migration.LastStage = migration.State
	migration.State = MigrationFailedRetryable
	migration.RetryCount++
	migration.Error = err.Error()
	migration.ErrorCode = lanceErrorCode(err)
	migration.FinishedAt = time.Now().UnixMilli()
	_ = control.UpdateMigration(ctx, migration)
	_ = control.SetGenerationState(ctx, generation.ID, GenerationFailed, err.Error())
	return failedRefresh(generation.AgentKey, "migration", err), err
}

func migrationImportCanResume(migration *Migration) bool {
	if migration == nil {
		return false
	}
	stage := strings.TrimSpace(migration.LastStage)
	if stage == "" {
		stage = strings.TrimSpace(migration.State)
		// Early schema-v3 builds did not persist LastStage. Their retryable
		// records represented import interruptions, so preserve that upgrade
		// path while new failures record the exact stage.
		if stage == MigrationFailedRetryable {
			return true
		}
	}
	return stage == MigrationPending || stage == MigrationSnapshotting || stage == MigrationImporting
}

func lanceErrorCode(err error) string {
	var engineErr *LanceEngineError
	if errors.As(err, &engineErr) && engineErr.Code != "" {
		return engineErr.Code
	}
	if errors.Is(err, errLegacyRequiresRebuild) {
		return "legacy_requires_rebuild"
	}
	return "engine_internal"
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

func resolvedLanceConfig(cfg resolvedConfig) resolvedConfig {
	cfg.IndexHash = computeIndexHashForSchema(cfg, ControlSchemaVersion)
	cfg.ConfigHash = cfg.IndexHash
	cfg.QueryHash = computeQueryHash(cfg)
	return cfg
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
		"legacyNeedsRefresh":   "true",
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

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func (m *Manager) selectRetrieval(ctx context.Context, cfg resolvedConfig) (RetrievalStore, string, bool, error) {
	mode := m.engineMode()
	if mode != "sqlite" {
		control, err := OpenReadControlStore(cfg.StorageDir)
		if err == nil {
			active, activeErr := control.ActiveGeneration(ctx)
			_ = control.Close()
			if activeErr != nil {
				return nil, "", false, activeErr
			}
			if active != nil {
				if err := m.registerLanceGeneration(ctx, cfg, active); err != nil {
					return nil, "", false, err
				}
				return m.lance, active.ID, true, nil
			}
		} else if !os.IsNotExist(err) {
			return nil, "", false, err
		}
		if mode == "lancedb" {
			return nil, "", false, &PolicyError{Kind: ErrorUnavailable, Message: "KBASE LanceDB generation is not ready"}
		}
	}
	if !fileExists(filepath.Join(cfg.StorageDir, "kbase.db")) {
		return nil, "", false, nil
	}
	store, err := OpenReadStore(cfg.StorageDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", false, nil
		}
		return nil, "", false, err
	}
	ready := storedIndexHash(store, cfg.StorageDir) != ""
	_ = store.Close()
	if !ready {
		return nil, "", false, nil
	}
	return NewSQLiteRetrievalStore(cfg.StorageDir), "legacy", true, nil
}

func legacyRefreshRequired(storageDir string) bool {
	control, err := OpenReadControlStore(storageDir)
	if err != nil {
		return false
	}
	defer control.Close()
	value, err := control.Meta(context.Background(), "legacyNeedsRefresh")
	return err == nil && strings.EqualFold(strings.TrimSpace(value), "true")
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
	if m.engineMode() == "sqlite" {
		return nil, &PolicyError{Kind: ErrorUnavailable, Message: "generation rollback requires the LanceDB engine"}
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
	if err := m.registerLanceGeneration(ctx, resolvedLanceConfig(cfg), target); err != nil {
		return nil, err
	}
	validation, err := m.lance.Validate(ctx, target.ID)
	if err != nil || !validation.Ready {
		if err == nil {
			err = fmt.Errorf("rollback generation validation failed")
		}
		return nil, err
	}
	if err := activateGeneration(ctx, control, resolvedLanceConfig(cfg), target); err != nil {
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

func (m *Manager) maybeShadowLiveQuery(cfg resolvedConfig, req RetrievalRequest, current RetrievalResponse) {
	percent := m.options.Migration.ShadowLivePercent
	if percent <= 0 || !fileExists(filepath.Join(cfg.StorageDir, "kbase.db")) {
		return
	}
	digest := sha256.Sum256([]byte(req.Query))
	if int(digest[0])%100 >= percent {
		return
	}
	maxQueries := m.options.Migration.MaxReplayQueries
	if maxQueries <= 0 {
		return
	}
	m.mu.Lock()
	if m.shadowQueries[cfg.AgentKey] >= maxQueries {
		m.mu.Unlock()
		return
	}
	m.shadowQueries[cfg.AgentKey]++
	m.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		legacy, err := NewSQLiteRetrievalStore(cfg.StorageDir).Search(ctx, "legacy", req)
		if err != nil {
			log.Printf("[kbase-lance] agentKey=%s operation=shadow queryLength=%d queryHash=%x error=%v",
				cfg.AgentKey, len([]rune(req.Query)), digest[:6], err)
			return
		}
		currentIDs := make(map[string]struct{}, len(current.Matches))
		for _, match := range current.Matches {
			currentIDs[match.Chunk.ID] = struct{}{}
		}
		overlap := 0
		for _, match := range legacy.Matches {
			if _, ok := currentIDs[match.Chunk.ID]; ok {
				overlap++
			}
		}
		denominator := minInt(len(legacy.Matches), maxInt(1, req.Limit))
		ratio := 1.0
		if denominator > 0 {
			ratio = float64(overlap) / float64(denominator)
		}
		log.Printf("[kbase-lance] agentKey=%s operation=shadow queryLength=%d queryHash=%x overlap=%.3f legacyCount=%d lanceCount=%d",
			cfg.AgentKey, len([]rune(req.Query)), digest[:6], ratio, len(legacy.Matches), len(current.Matches))
	}()
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

var errLegacyRequiresRebuild = errors.New("legacy KBASE requires a cold rebuild")
