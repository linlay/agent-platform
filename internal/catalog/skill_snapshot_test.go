package catalog

import (
	"agent-platform/internal/config"
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSkillSnapshotIncludesHiddenFilesAndIgnoresTimestamps(t *testing.T) {
	root := t.TempDir()
	r := &FileRegistry{cfg: config.Config{Paths: config.PathsConfig{SkillsCenterDir: root}}}
	missing, err := r.SnapshotEditableSkill("demo")
	if err != nil || missing.Exists || missing.Revision != "missing" {
		t.Fatalf("missing: %#v %v", missing, err)
	}
	dir := filepath.Join(root, "demo")
	if err := os.MkdirAll(filepath.Join(dir, "empty"), 0755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"SKILL.md": "invalid legacy skill without frontmatter", "skill.json": "{}", ".runtime-env.json": "{}", "script.sh": "echo test"}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0755); err != nil {
			t.Fatal(err)
		}
	}
	first, err := r.SnapshotEditableSkill("demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(dir, "SKILL.md"), time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	again, err := r.SnapshotEditableSkill("demo")
	if err != nil || first.Revision != again.Revision || !bytes.Equal(first.Archive, again.Archive) {
		t.Fatalf("unstable snapshot %v", err)
	}
	reader, err := zip.NewReader(bytes.NewReader(first.Archive), int64(len(first.Archive)))
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.File) != len(files)+1 {
		t.Fatalf("archive incomplete: %d", len(reader.File))
	}
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(rc)
		rc.Close()
		if err != nil || string(content) != files[file.Name] {
			t.Fatalf("archive content %s: %q %v", file.Name, content, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "script.sh"), []byte("changed"), 0755); err != nil {
		t.Fatal(err)
	}
	changed, err := r.SnapshotEditableSkill("demo")
	if err != nil || first.Revision == changed.Revision {
		t.Fatalf("revision did not change %v", err)
	}
}

func TestSkillSnapshotRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "demo")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(dir, "external")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	r := &FileRegistry{cfg: config.Config{Paths: config.PathsConfig{SkillsCenterDir: root}}}
	if _, err := r.SnapshotEditableSkill("demo"); err != ErrSkillSymlink {
		t.Fatalf("expected symlink refusal, got %v", err)
	}
}
