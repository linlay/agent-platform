package kbase

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (s *generationService) RegisterGeneration(ctx context.Context, cfg resolvedConfig, generation *Generation) error {
	if s == nil || s.runtime == nil || s.runtime.store == nil || generation == nil {
		return fmt.Errorf("kbase lance engine is not configured")
	}
	if err := s.runtime.store.CreateGeneration(ctx, GenerationSpec{
		AgentKey: generation.AgentKey, GenerationID: generation.ID, StorageDir: cfg.StorageDir,
		EmbeddingModel: generation.EmbeddingModel, EmbeddingDimension: generation.EmbeddingDimension,
		FTSBaseTokenizer: firstNonBlank(generation.FTSTokenizer, "icu"),
	}); err != nil {
		return err
	}
	return writeGenerationManifest(cfg.StorageDir, generation)
}

func (s *generationService) SelectRetrieval(ctx context.Context, cfg resolvedConfig) (RetrievalStore, string, bool, error) {
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
	if err := s.RegisterGeneration(ctx, cfg, active); err != nil {
		return nil, "", false, &PolicyError{Kind: ErrorUnavailable, Message: "KBASE LanceDB sidecar is unavailable: " + err.Error()}
	}
	return s.runtime.store, active.ID, true, nil
}

func (s *generationService) ReleaseStorageGeneration(agentKey, storageDir string) {
	if s == nil || s.runtime == nil || s.runtime.store == nil || strings.TrimSpace(storageDir) == "" || !s.runtime.State().Available {
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
	if _, err := s.runtime.store.ReleaseGeneration(ctx, generation.ID, agentKey); err != nil {
		log.Printf("[kbase-lance] release generation failed agent=%s generation=%s: %v", agentKey, generation.ID, err)
	}
}

func (s *generationService) Rollback(ctx context.Context, cfg resolvedConfig, generationID string) (*Generation, error) {
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
	if err := s.RegisterGeneration(ctx, cfg, target); err != nil {
		return nil, err
	}
	validation, err := s.runtime.store.Validate(ctx, target.ID)
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

func activeGenerationMeta(cfg resolvedConfig, generation *Generation) map[string]string {
	return map[string]string{
		"schemaVersion": ControlSchemaVersion, "engine": "lancedb", "indexHash": generation.IndexHash,
		"queryHash": desiredQueryHash(cfg), "configHash": generation.IndexHash,
		"embeddingModelKey": generation.EmbeddingModelKey, "embeddingProviderKey": generation.EmbeddingProviderKey,
		"embeddingModel": generation.EmbeddingModel, "embeddingDimension": strconv.Itoa(generation.EmbeddingDimension),
		"ftsTokenizer": firstNonBlank(generation.FTSTokenizer, "icu"), "lastIndexedAt": strconv.FormatInt(time.Now().UnixMilli(), 10),
	}
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
	if err := control.SetMeta(ctx, "activeGeneration", generation.ID); err != nil {
		return err
	}
	for key, value := range activeGenerationMeta(cfg, generation) {
		if err := control.SetMeta(ctx, key, value); err != nil {
			return err
		}
	}
	return nil
}

func firstNonNil(err error, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}
