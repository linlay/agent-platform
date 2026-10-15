package kbx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/knowledge"
)

func (m *Manager) ReadBound(_ string, libraryID string, options knowledge.ReadOptions) (knowledge.ReadResult, error) {
	return m.readLibrary(libraryID, options)
}

// Agent binding is not an authorization source: the caller verifies Chat
// access and publication.
// OpenBoundSource opens a published document in the selected library, never an
// arbitrary host path. os.Root enforces the source boundary while opening links.
func (m *Manager) OpenBoundSource(_ string, libraryID, p string) (*os.File, error) {
	l, err := m.resolveLibrary(libraryID)
	if err != nil {
		return nil, err
	}
	defer l.release()
	if _, err = allowedDocumentPath(l, "kbx://"+p); err != nil {
		return nil, err
	}
	name, relative, _ := strings.Cut(p, "/")
	for _, c := range l.definition.Collections {
		if c.Name == name {
			sourcePath := filepath.Join(c.SourcePath, relative)
			canonical, err := filepath.EvalSymlinks(sourcePath)
			if err != nil || canonical != sourcePath {
				return nil, fmt.Errorf("source path changed or is a symbolic link")
			}
			root, err := os.OpenRoot(c.SourcePath)
			if err != nil {
				return nil, err
			}
			defer root.Close()
			f, err := root.Open(relative)
			if err != nil {
				return nil, err
			}
			st, err := f.Stat()
			if err != nil || !st.Mode().IsRegular() {
				f.Close()
				return nil, fmt.Errorf("source is not a regular file")
			}
			return f, nil
		}
	}
	return nil, fmt.Errorf("source collection not found")
}
