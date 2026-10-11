package kbx

import (
	"agent-platform/internal/kbases"
	"agent-platform/internal/knowledge"
	"errors"
	"strings"
)

func (m *Manager) Status(key string) (knowledge.Status, error) {
	key = strings.TrimSpace(key)
	if m.options.KBases == nil {
		return knowledge.Status{}, unavailable("knowledge library service unavailable")
	}
	d, err := m.options.KBases.Get(key)
	if err != nil {
		if errors.Is(err, kbases.ErrNotFound) {
			return knowledge.Status{}, &knowledge.PolicyError{Kind: knowledge.ErrorNotFound, Message: "knowledge library not found: " + key}
		}
		return knowledge.Status{}, unavailable(err.Error())
	}
	status := knowledge.Status{LibraryID: key, Mode: knowledge.Mode, Engine: "kbx", State: d.State, Indexing: d.Indexing, Stale: d.Stale, Degraded: d.Degraded, Error: d.Error, StorageLocation: "library"}
	if d.RefreshError != "" {
		status.Error = d.RefreshError
	}
	if d.IndexedAt > 0 {
		status.LastIndexedAt = &d.IndexedAt
	}
	modelKey := m.options.DefaultEmbeddingModelKey
	if m.options.ConfigSource != nil {
		modelKey, _ = m.options.ConfigSource.selection(d)
	} else if d.Models != nil && d.Models.Embedding != nil {
		modelKey = d.Models.Embedding.ModelKey
	}
	status.Indexes = &knowledge.IndexesStatus{FTS: knowledge.IndexStatus{Type: "fts", Ready: d.IndexedAt > 0}, Vector: knowledge.VectorIndexStatus{Type: "vector", Ready: d.IndexedAt > 0 && !d.Degraded && modelKey != "" && !d.VectorsPending}}
	if d.IndexedAt > 0 {
		l, e := m.resolveLibrary(key)
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

// AgentStatus preserves the existing Agent status REST contract.
func (m *Manager) AgentStatus(key string) (knowledge.Status, error) {
	spec, err := m.boundAgent(key)
	if err != nil {
		return knowledge.Status{}, err
	}
	status, err := m.Status(spec.Config.LibraryID)
	status.AgentKey, status.WorkspaceRoot = key, spec.WorkspaceRoot
	return status, err
}
