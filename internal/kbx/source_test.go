package kbx

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/knowledge"
)

func TestSourceReadBindingAndTraversal(t *testing.T) {
	m, l := newTestManager(t)
	os.WriteFile(filepath.Join(l.spec.WorkspaceRoot, "note.md"), []byte("source"), 0600)
	f, err := m.OpenBoundSource("docs", l.spec.Config.LibraryID, "workspace/note.md")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	for _, p := range []string{"../note.md", "workspace/../note.md", "foreign/note.md", "workspace/.private/secret.md"} {
		if f, err = m.OpenBoundSource("docs", l.spec.Config.LibraryID, p); err == nil {
			f.Close()
			t.Fatal("escape accepted", p)
		}
	}
	m.runner = runFunc(func(context.Context, string, []byte, ...string) ([]byte, error) {
		t.Fatal("binding mismatch invoked CLI")
		return nil, nil
	})
	if _, err = m.ReadBound("docs", "previous", knowledge.ReadOptions{Path: "workspace/note.md"}); err == nil {
		t.Fatal("old binding accepted")
	}
	outside := filepath.Join(t.TempDir(), "private.md")
	os.WriteFile(outside, []byte("secret"), 0600)
	os.Symlink(outside, filepath.Join(l.spec.WorkspaceRoot, "link.md"))
	if f, err = m.OpenBoundSource("docs", l.spec.Config.LibraryID, "workspace/link.md"); err == nil {
		f.Close()
		t.Fatal("symlink accepted")
	}
}
