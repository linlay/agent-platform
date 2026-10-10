package adminsource

import (
	"errors"
	"fmt"

	"agent-platform/internal/contracts"
	"agent-platform/internal/kbases"
)

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
