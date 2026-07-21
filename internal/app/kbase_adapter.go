package app

import (
	"strings"

	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/kbase"
)

// kbaseCatalogSource is the app-owned mapping from the broad catalog snapshot
// into the mode-neutral capability specs consumed by the KBASE runtime.
type kbaseCatalogSource struct {
	registry *catalog.FileRegistry
}

func (s kbaseCatalogSource) Agents() []kbase.AgentSpec {
	if s.registry == nil {
		return nil
	}
	keys := s.registry.AdminAgentKeys()
	out := make([]kbase.AgentSpec, 0, len(keys))
	for _, key := range keys {
		if spec, ok := s.Agent(key); ok {
			out = append(out, spec)
		}
	}
	return out
}

func (s kbaseCatalogSource) Agent(key string) (kbase.AgentSpec, bool) {
	if s.registry == nil {
		return kbase.AgentSpec{}, false
	}
	definition, ok := s.registry.AgentDefinition(strings.TrimSpace(key))
	if !ok {
		return kbase.AgentSpec{}, false
	}
	return kbase.AgentSpec{
		Key:         definition.Key,
		Enabled:     definition.KBaseConfig.Enabled,
		SourceRoot:  definition.KBaseConfig.Source.Root,
		Requirement: definition.KBaseRequirement,
		Config:      definition.KBaseConfig,
	}, true
}

func kbaseManagerOptions(cfg config.Config) kbase.ManagerOptions {
	extraction := cfg.KBase.Extraction
	return kbase.ManagerOptions{
		RuntimeDir:               cfg.Paths.KBaseDir,
		DefaultEmbeddingModelKey: cfg.KBase.Embedding.ModelKey,
		RefreshDebounce:          cfg.KBase.Refresh.Debounce,
		ReconcileInterval:        cfg.KBase.Refresh.ReconcileInterval,
		Index: kbase.IndexOptions{
			FTSBaseTokenizer: cfg.KBase.Index.FTS.BaseTokenizer,
			ANNMinRows:       cfg.KBase.Index.Vector.ANNMinRows,
		},
		Maintenance: kbase.MaintenanceOptions{
			OptimizeChangeThreshold: cfg.KBase.Maintenance.OptimizeChangeThreshold,
			OptimizeInterval:        cfg.KBase.Maintenance.OptimizeInterval,
			VersionRetention:        cfg.KBase.Maintenance.VersionRetention,
		},
		Extraction: kbase.ExtractionConfig{
			Timeout:      extraction.Timeout,
			MaxFileBytes: extraction.MaxFileBytes,
			PDF: kbase.PDFExtractionConfig{
				Enabled: extraction.PDF.Enabled,
				Backend: extraction.PDF.Backend,
				Binary:  extraction.PDF.Binary,
			},
			DOCX: kbase.DOCXExtractionConfig{
				Enabled: extraction.DOCX.Enabled,
				Backend: extraction.DOCX.Backend,
			},
			PPTX: kbase.PPTXExtractionConfig{
				Enabled:      extraction.PPTX.Enabled,
				Backend:      extraction.PPTX.Backend,
				IncludeNotes: extraction.PPTX.IncludeNotes,
			},
		},
	}
}
