package catalog

import (
	"agent-platform/internal/config"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOverwritePackageChildPreservesOwnershipRecord(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills-center")
	registry := &FileRegistry{cfg: config.Config{Paths: config.PathsConfig{SkillsCenterDir: root}}}
	archive := buildSkillPackageZIP(t, "test-pack", "1.0.0", []testSkillPackageEntry{{ID: "test-skill", Version: "1.0.0", Present: true}})
	pkg, _, err := registry.BeginImportEditableSkillPackageArchive("test-pack", "1.0.0", bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	if err := pkg.Commit(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".package", "test-pack.json")
	previous, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	archive = buildSkillImportZIP(t, []skillImportZIPEntry{{name: "SKILL.md", content: []byte("---\nname: test-skill\ndescription: Child updated\n---\n\nUpdated.\n")}, {name: "skill.json", content: []byte(`{"version":"2.0.0"}`)}})
	mutation, _, err := registry.BeginImportEditableSkillArchive("test-skill", bytes.NewReader(archive), int64(len(archive)), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := mutation.Commit(); err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(previous, current) {
		t.Fatalf("package record changed: %s %v", current, err)
	}
	if owners, err := readSkillPackageOwners(root); err != nil || owners["test-skill"] != "test-pack" {
		t.Fatalf("ownership changed: %v %v", owners, err)
	}
}

func TestPreparedSkillRechecksDestinationAtPublication(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills-center")
	r := &FileRegistry{cfg: config.Config{Paths: config.PathsConfig{SkillsCenterDir: root}}}
	archive := buildSkillImportZIP(t, []skillImportZIPEntry{{name: "SKILL.md", content: []byte("---\nname: demo\ndescription: Prepared\n---\nBody\n")}})
	prepared, err := r.PrepareEditableSkillArchive("demo", bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	if _, err := os.Stat(filepath.Join(root, "demo")); !os.IsNotExist(err) {
		t.Fatalf("prepare published source: %v", err)
	}
	// Another request publishes while this candidate is being prepared.
	installed, _, err := r.BeginImportEditableSkillArchive("demo", bytes.NewReader(archive), int64(len(archive)), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := installed.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepared.Begin(false); !errors.Is(err, ErrSkillAlreadyExists) {
		t.Fatalf("missed late conflict: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "demo", "SKILL.md"))
	if err != nil || !bytes.Contains(data, []byte("Prepared")) {
		t.Fatalf("existing source lost: %v", err)
	}
}

func TestPreparedSkillPackageRechecksOwnershipAtPublication(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills-center")
	r := &FileRegistry{cfg: config.Config{Paths: config.PathsConfig{SkillsCenterDir: root}}}
	archive := buildSkillPackageZIP(t, "test-pack", "1.0.0", []testSkillPackageEntry{{ID: "test-skill", Version: "1.0.0", Present: true}})
	prepared, err := r.PrepareEditableSkillPackageArchive("test-pack", "1.0.0", bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	if err := os.MkdirAll(filepath.Join(root, "test-skill"), 0755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(root, "test-skill", "sentinel")
	if err := os.WriteFile(sentinel, []byte("independent skill"), 0644); err != nil {
		t.Fatal(err)
	}
	recordPath, err := skillPackageRecordPath(root, "other-pack")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSkillPackageRecordFile(recordPath, []byte(`{"schemaVersion":1,"id":"other-pack","version":"1.0.0","sha256":"test","installedAt":1,"skills":[{"id":"test-skill","version":"1.0.0"}]}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepared.Begin(); !errors.Is(err, ErrSkillPackageConflict) {
		t.Fatalf("missed late owner conflict: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatal("removed independent skill")
	}
}
