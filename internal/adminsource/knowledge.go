package adminsource

import (
	"errors"
	"fmt"

	agentkbase "agent-platform/internal/agent/kbase"
	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
	"agent-platform/internal/kbases"
	"agent-platform/internal/knowledge"
)

// ValidateKnowledgeBinding belongs at management entry points. Catalog loading
// checks the mode requirement without depending on the library service.
func ValidateKnowledgeBinding(definition map[string]any, validate func(string) error) error {
	mode, engine, err := catalog.ParseAgentModeAndEngine(contracts.AnyStringNode(definition["mode"]), contracts.AnyStringNode(definition["engine"]))
	if err != nil {
		return err
	}
	cfg, err := knowledge.ParseConfig(contracts.AnyMapNode(definition["kbaseConfig"]))
	if err != nil {
		return err
	}
	if mode == agentkbase.Mode && engine != catalog.AgentEngineACP {
		if err := agentkbase.ValidateLibraryID(cfg.LibraryID); err != nil {
			return err
		}
	}
	if cfg.LibraryID == "" {
		return nil
	}
	if validate == nil {
		return fmt.Errorf("knowledge library service unavailable; cannot verify kbaseConfig.libraryId")
	}
	if err := validate(cfg.LibraryID); err != nil {
		return fmt.Errorf("invalid kbaseConfig.libraryId %q: %w; repair the library definition or bind a valid library before saving this Agent", cfg.LibraryID, err)
	}
	return nil
}

// PrepareKnowledgeBinding owns creation rollback. The caller commits after the
// Agent definition has been saved, even if a subsequent catalog reload fails.
func (s *Service) PrepareKnowledgeBinding(libraryService *kbases.Service, definition map[string]any, name, source string) (map[string]any, func(bool) error, error) {
	if libraryService == nil {
		return nil, nil, fmt.Errorf("knowledge library service unavailable")
	}
	if len(contracts.AnyMapNode(definition["kbaseConfig"])) > 0 {
		return nil, nil, fmt.Errorf("createLibrary and kbaseConfig are mutually exclusive")
	}
	d, release, err := libraryService.CreateHeld(kbases.Input{Name: name, SourcePath: source})
	if err != nil {
		return nil, nil, err
	}
	out := contracts.CloneMap(definition)
	out["kbaseConfig"] = map[string]any{"libraryId": d.ID}
	return out, func(committed bool) error {
		defer release()
		if committed {
			return nil
		}
		err := libraryService.Delete(d.ID)
		if errors.Is(err, kbases.ErrNotFound) {
			return nil
		}
		return err
	}, nil
}
