package kbx

import (
	"agent-platform/internal/knowledge"
	"path/filepath"
)

func (m *Manager) Status(key string) (knowledge.Status, error) {
	l, err := m.resolve(key)
	if err != nil {
		return knowledge.Status{}, err
	}
	s := knowledge.Status{AgentKey: key, Mode: knowledge.Mode, Engine: "kbx", StorageDir: filepath.Dir(l.database), StorageLocation: l.spec.Config.Storage.Location, WorkspaceRoot: l.spec.WorkspaceRoot, Stale: true, Degraded: true, State: "unindexed", Chunk: knowledge.ChunkConfig{Unit: "chars", MaxChars: 3600, OverlapChars: 540}}
	if m.workerStatus(l, &s) {
		return s, nil
	}
	ctx, cancel := readerContext()
	defer cancel()
	var response struct {
		Documents int
		Chunking  map[string]struct {
			Effective struct {
				MaxChars     int `json:"max_chars"`
				OverlapChars int `json:"overlap_chars"`
			}
		}
		Capabilities struct {
			Vector struct {
				Complete   bool
				Dimensions int
			}
		}
	}
	if err = m.call(ctx, l, false, &response, "status", "--agent"); err != nil {
		s.Error = err.Error()
		return s, nil
	}
	s.Files = response.Documents
	s.FileStats.Active = response.Documents
	s.Embedding.Dimension = response.Capabilities.Vector.Dimensions
	if c, ok := response.Chunking["workspace"]; ok {
		s.Chunk.MaxChars = c.Effective.MaxChars
		s.Chunk.OverlapChars = c.Effective.OverlapChars
	}
	// KBX agent status does not expose a chunk count. Do not report a fake zero.
	known := false
	s.ChunksKnown = &known
	return s, nil
}

// Worker state describes source synchronization; KBX index readiness alone
// cannot tell whether a registered empty library has ever scanned its source.
func (m *Manager) workerStatus(l library, s *knowledge.Status) bool {
	m.mu.Lock()
	w := m.workers[l.spec.Key]
	startErr := m.startError
	m.mu.Unlock()
	if startErr != nil {
		s.State = "error"
		s.Stale = true
		s.Degraded = true
		s.Error = startErr.Error()
		return true
	}
	if w == nil || w.library.database != l.database {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	s.Degraded = false
	s.RefreshID = w.latest
	s.Indexing = w.running || len(w.queue) > 0 || !w.dirtyAt.IsZero()
	s.PendingChanges = len(w.paths)
	if w.full {
		s.PendingChanges++
	}
	s.Stale = !w.initialized || s.Indexing || w.lastError != ""
	s.Error = w.lastError
	s.LastIndexedAt = w.indexedAt
	s.State = "unindexed"
	if w.initialized {
		s.State = "ready"
	}
	if w.index != nil {
		s.Files = w.index.Documents
		s.FileStats.Active = s.Files
		embeddingConfigured := m.options.DefaultEmbeddingModelKey != "" || (m.options.ConfigSource != nil && m.options.ConfigSource.ModelKey != "")
		s.Indexes = &knowledge.IndexesStatus{FTS: knowledge.IndexStatus{Type: "fts", Ready: w.initialized && w.index.FullText.Ready}, Vector: knowledge.VectorIndexStatus{Type: "vector", Ready: embeddingConfigured && w.index.Vector.Complete && (w.index.Vector.Compatible == nil || *w.index.Vector.Compatible), PendingContentUnits: &w.index.Vector.Pending}}
		s.Degraded = s.Files > 0 && !s.Indexes.Vector.Ready
	}
	if s.Degraded {
		s.State = "degraded"
	}
	if w.lastError != "" {
		s.State = "error"
		s.Degraded = true
		if s.Indexes != nil && s.Indexes.FTS.Ready {
			s.State = "degraded"
		}
	}
	if w.watchError != "" {
		s.Degraded = true
		if s.Error == "" {
			s.Error = w.watchError
		}
		if s.State == "ready" {
			s.State = "degraded"
		}
	}
	if s.Indexing {
		if w.initialized {
			s.State = "refreshing"
		} else {
			s.State = "indexing"
		}
	}
	if l.spec.Config.Chunk.Unit == knowledge.ChunkUnitChars {
		s.Chunk = l.spec.Config.Chunk
	}
	return true
}

type readinessError struct {
	*knowledge.PolicyError
	state knowledge.Status
}

func (e *readinessError) Unwrap() error { return e.PolicyError }
func (e *readinessError) KnowledgeBaseState() map[string]any {
	return map[string]any{"refreshId": e.state.RefreshID, "indexing": e.state.Indexing, "state": e.state.State}
}
