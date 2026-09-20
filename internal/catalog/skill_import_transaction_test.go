package catalog

import (
	"agent-platform/internal/config"
	"bytes"
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
