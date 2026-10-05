package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareSummaryPreservesExistingMarkdownAndPendingWrites(t *testing.T) {
	s := testStore(t)
	if err := os.MkdirAll(s.MemoryDir, 0700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(s.MemoryDir, "memory.md")
	if err := os.WriteFile(old, []byte("原有人工记忆"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareSummary(); err != nil {
		t.Fatal(err)
	}
	d, err := s.Read("summary", "")
	if err != nil || d.Content != "原有人工记忆" {
		t.Fatal(d, err)
	}
	alias, _ := s.Read("memory", "")
	if alias.Revision != d.Revision {
		t.Fatal("alias diverged")
	}
	if _, err = s.Save("summary", "", "新内容", d.Revision); err != nil {
		t.Fatal(err)
	}
	if err = s.PrepareSummary(); err != nil {
		t.Fatal(err)
	}
	d, _ = s.Read("memory", "")
	if d.Content != "新内容" {
		t.Fatal("legacy restored over current summary")
	}
	if _, err = s.Delete("memory", "", d.Revision); err != nil {
		t.Fatal(err)
	}
	if err = s.PrepareSummary(); err != nil {
		t.Fatal(err)
	}
	d, _ = s.Read("memory", "")
	if d.Exists {
		t.Fatal("deleted summary resurrected from legacy backup")
	}
	if err = os.WriteFile(filepath.Join(s.MemoryDir, ".memx-pending.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Save("memory", "", "overwritten", d.Revision); err != ErrConflict {
		t.Fatal(err)
	}
}
