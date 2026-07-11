package app

import (
	"strings"

	"agent-platform/internal/agent/kbase"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
)

// kbaseCatalogSource is the app-owned mapping from the broad catalog into the
// narrow, mode-owned source consumed by KBASE.
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
		Key:           definition.Key,
		Mode:          definition.Mode,
		WorkspaceRoot: definition.Workspace.Root,
		Config:        definition.KBaseConfig,
	}, true
}

func kbaseManagerOptions(cfg config.Config) kbase.ManagerOptions {
	extraction := cfg.KBase.Extraction
	return kbase.ManagerOptions{
		RuntimeDir:               cfg.Paths.KBaseDir,
		DefaultEmbeddingModelKey: cfg.KBase.Embedding.ModelKey,
		RefreshDebounce:          cfg.KBase.Refresh.Debounce,
		ReconcileInterval:        cfg.KBase.Refresh.ReconcileInterval,
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
