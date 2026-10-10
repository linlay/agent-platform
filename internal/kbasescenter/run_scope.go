package kbasescenter

import "agent-platform/internal/knowledge"

// RunCollections takes an independent snapshot; it never holds a library lock
// for the lifetime of a Run. Later library edits affect the next admission.
func (s *Service) RunCollections(id string) ([]knowledge.CollectionScope, error) {
	lock := s.libraryLock(id)
	lock.RLock()
	defer lock.RUnlock()
	d, err := s.loadConfiguration(id, true)
	if err != nil {
		return nil, err
	}
	out := make([]knowledge.CollectionScope, 0, len(d.Collections))
	for _, c := range d.Collections {
		out = append(out, knowledge.CollectionScope{Name: c.Name, SourcePath: c.SourcePath, Description: c.Description, Editable: c.Editable})
	}
	return out, nil
}
