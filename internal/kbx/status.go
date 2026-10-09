package kbx

import (
	"agent-platform/internal/knowledge"
)

func (m *Manager) Status(key string) (knowledge.Status, error) {
	spec, err := m.boundAgent(key)
	if err != nil {
		return knowledge.Status{}, err
	}
	d, err := m.options.Center.Get(spec.Config.LibraryID)
	if err != nil {
		return knowledge.Status{}, unavailable(err.Error())
	}
	status := knowledge.Status{AgentKey: key, LibraryID: spec.Config.LibraryID, Mode: knowledge.Mode, Engine: "kbx", State: d.State, Indexing: d.Indexing, Stale: d.Stale, Degraded: d.Degraded, Error: d.Error, StorageLocation: "library", WorkspaceRoot: spec.WorkspaceRoot}
	if d.RefreshError != "" {
		status.Error = d.RefreshError
	}
	if d.IndexedAt > 0 {
		status.LastIndexedAt = &d.IndexedAt
	}
	status.Indexes = &knowledge.IndexesStatus{FTS: knowledge.IndexStatus{Type: "fts", Ready: d.IndexedAt > 0}, Vector: knowledge.VectorIndexStatus{Type: "vector", Ready: d.IndexedAt > 0 && !d.Degraded && m.options.ConfigSource != nil && m.options.ConfigSource.ModelKey != ""}}
	if d.IndexedAt > 0 {
		l, e := m.resolve(key)
		if e != nil {
			return status, e
		}
		defer l.release()
		ctx, cancel := readerContext()
		defer cancel()
		var actual struct {
			Documents    int
			Capabilities struct {
				Vector struct {
					Complete   bool
					Dimensions int
				}
			}
		}
		if e = m.call(ctx, l, false, &actual, "status", "--agent"); e != nil {
			status.Error = e.Error()
			status.Degraded = true
			status.Indexes.FTS.Ready = false
			status.Indexes.Vector.Ready = false
		} else {
			status.Files = actual.Documents
			status.FileStats.Active = actual.Documents
			status.Embedding.Dimension = actual.Capabilities.Vector.Dimensions
			status.Indexes.Vector.Ready = status.Indexes.Vector.Ready && actual.Capabilities.Vector.Complete
		}
	}
	return status, nil
}
