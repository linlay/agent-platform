package kbx

import (
	"path/filepath"
	"testing"
)

func TestSharedIndexFlatPath(t *testing.T) {
	m, l := newTestManager(t)
	if filepath.Base(filepath.Dir(l.database)) != l.spec.Config.LibraryID {
		t.Fatal(l.database)
	}
	a := l.spec
	a.Key = "other"
	a.WorkspaceRoot = t.TempDir()
	m.agents.(testSource)["other"] = a
	n, err := m.resolve("other")
	if err != nil {
		t.Fatal(err)
	}
	defer n.release()
	if n.database != l.database {
		t.Fatal("agent duplicated shared index")
	}
}
