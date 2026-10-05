package kbx

import (
	"agent-platform/internal/kbase"
	"path/filepath"
)

func (m *Manager) Status(key string) (kbase.Status, error) {
	l, err := m.resolve(key)
	if err != nil {
		return kbase.Status{}, err
	}
	s := kbase.Status{AgentKey: key, Mode: kbase.Mode, Engine: "kbx", StorageDir: filepath.Dir(l.database), StorageLocation: l.spec.Config.Storage.Location, WorkspaceRoot: l.spec.WorkspaceRoot, Stale: true, Degraded: true, Error: updateUnavailable, Chunk: kbase.ChunkConfig{Unit: "chars", MaxChars: 3600, OverlapChars: 540}}
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
