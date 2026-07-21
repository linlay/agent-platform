package kbase

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"agent-platform/internal/timecontract"
)

type statusService struct {
	resolver    *capabilityResolver
	state       *capabilityState
	generations *generationService
	refresh     *refreshCoordinator
	lance       *lanceRuntime
}

func newStatusService(resolver *capabilityResolver, state *capabilityState, generations *generationService, refresh *refreshCoordinator, lance *lanceRuntime) *statusService {
	return &statusService{resolver: resolver, state: state, generations: generations, refresh: refresh, lance: lance}
}

func (s *statusService) Status(agentKey string) (Status, error) {
	if failure := s.state.Failure(agentKey); failure != nil {
		spec, specErr := s.resolver.AgentSpec(agentKey)
		if specErr != nil {
			return Status{AgentKey: agentKey, Mode: Mode}, specErr
		}
		return Status{
			AgentKey: spec.Key, Mode: Mode, SourceRoot: spec.Config.Source.Root, WorkspaceRoot: spec.Config.Source.Root,
			StorageLocation: spec.Config.Storage.Location, StorageDir: s.resolver.StorageDirForSpec(spec),
			Stale: true, Degraded: true, Error: failure.Error(), Engine: "lancedb", SchemaVersion: ControlSchemaVersion,
		}, nil
	}
	cfg, _, err := s.resolver.Resolve(agentKey)
	if err != nil {
		return Status{AgentKey: agentKey, Mode: Mode}, err
	}
	status := Status{
		AgentKey: cfg.AgentKey, Mode: Mode, StorageLocation: cfg.Storage, StorageDir: cfg.StorageDir,
		SourceRoot: cfg.WorkspaceRoot, WorkspaceRoot: cfg.WorkspaceRoot, Embedding: cfg.Embedding, Chunk: cfg.Chunk,
		Indexing: s.refresh.IsIndexing(cfg.AgentKey, cfg.StorageDir), ConfigHash: desiredIndexHash(cfg),
		Engine: "lancedb", SchemaVersion: ControlSchemaVersion, StorageDiskUsage: storageDiskUsage(cfg.StorageDir),
		PendingChanges: s.refresh.PendingChanges(cfg.StorageDir),
	}
	control, err := OpenReadControlStore(cfg.StorageDir)
	if err != nil {
		if !os.IsNotExist(err) {
			return status, err
		}
		status.Stale = true
		state := s.lance.State()
		status.Sidecar = &state
		return validatePublicStatusTimes(status)
	}
	defer control.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	active, err := control.ActiveGeneration(ctx)
	if err != nil {
		return status, err
	}
	if active == nil {
		status.Stale = true
		state := s.lance.State()
		status.Sidecar = &state
		return validatePublicStatusTimes(status)
	}
	status.Generation = &GenerationStatus{ID: active.ID, State: active.State, TableVersion: active.TableVersion, CreatedAt: active.CreatedAt, ActivatedAt: active.ActivatedAt}
	status.ConfigHash = active.IndexHash
	status.Files, status.Chunks, _ = generationControlCounts(ctx, control, active.ID)
	status.FileStats, _ = control.FileStats(ctx, active.ID)
	status.Stale = active.IndexHash == "" || active.IndexHash != desiredIndexHash(cfg)
	if last, metaErr := control.Meta(ctx, "lastIndexedAt"); metaErr == nil {
		indexedAt, parseErr := parseOptionalPublicEpochMillis(last, "lastIndexedAt", "kbase.status.metadata")
		if parseErr != nil {
			return status, parseErr
		}
		status.LastIndexedAt = indexedAt
	}
	status.LastRun, _ = control.LastRun(ctx)
	if pending, pendingErr := control.PendingFileOperations(ctx, active.ID); pendingErr == nil {
		status.PendingRecoveryOps = len(pending)
	}
	registerErr := s.generations.RegisterGeneration(ctx, cfg, active)
	state := s.lance.State()
	if registerErr != nil {
		state.LastError, state.Available = registerErr.Error(), false
	}
	status.Sidecar = &state
	indexes := &IndexesStatus{}
	if stats, statsErr := s.lance.store.Stats(ctx, active.ID); statsErr == nil {
		indexes.FTS = IndexStatus{Type: firstNonBlank(stats.FTSIndexType, "FTS/ICU"), Ready: stats.FTSReady, UnindexedRows: stats.FTSUnindexedRows}
		indexes.Vector = VectorIndexStatus{Type: firstNonBlank(stats.VectorIndexType, "flat"), Ready: stats.VectorReady, UnindexedRows: stats.UnindexedRows}
		lastOptimized, _ := control.Meta(ctx, "lastOptimizedAt")
		optimizedAt, parseErr := parseOptionalPublicEpochMillis(lastOptimized, "lastOptimizedAt", "kbase.status.metadata")
		if parseErr != nil {
			return status, parseErr
		}
		indexes.LastOptimizedAt = optimizedAt
	}
	status.Indexes = indexes
	return validatePublicStatusTimes(status)
}

func parseOptionalPublicEpochMillis(raw, field, location string) (*int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil, &timecontract.Violation{Field: field, Location: location, Reason: "must be an unquoted epoch-ms integer"}
	}
	return timecontract.OptionalEpochMillis(value, field, location)
}

func validatePublicStatusTimes(status Status) (Status, error) {
	if status.LastIndexedAt != nil {
		if err := timecontract.ValidateEpochMillis(*status.LastIndexedAt, "lastIndexedAt", "kbase.status"); err != nil {
			return status, err
		}
	}
	if status.Generation != nil {
		if err := timecontract.ValidateEpochMillis(status.Generation.CreatedAt, "createdAt", "kbase.status.generation"); err != nil {
			return status, err
		}
		if status.Generation.ActivatedAt != 0 {
			if err := timecontract.ValidateEpochMillis(status.Generation.ActivatedAt, "activatedAt", "kbase.status.generation"); err != nil {
				return status, err
			}
		}
	}
	if status.Indexes != nil && status.Indexes.LastOptimizedAt != nil {
		if err := timecontract.ValidateEpochMillis(*status.Indexes.LastOptimizedAt, "lastOptimizedAt", "kbase.status.indexes"); err != nil {
			return status, err
		}
	}
	if status.LastRun != nil {
		if err := timecontract.ValidateEpochMillis(status.LastRun.StartedAt, "startedAt", "kbase.status.lastRun"); err != nil {
			return status, err
		}
		if status.LastRun.FinishedAt != 0 {
			if err := timecontract.ValidateEpochMillis(status.LastRun.FinishedAt, "finishedAt", "kbase.status.lastRun"); err != nil {
				return status, err
			}
		}
	}
	return status, nil
}

func storageDiskUsage(root string) int64 {
	var size int64
	_ = filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			if info, infoErr := entry.Info(); infoErr == nil {
				size += info.Size()
			}
		}
		return nil
	})
	return size
}
