package catalog

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/config"
)

type testSkillPackageEntry struct {
	ID       string
	Version  string
	Optional bool
	Present  bool
}

func TestEditableSkillPackageInstallUpdateDeleteAndRollback(t *testing.T) {
	root := t.TempDir()
	registry := &FileRegistry{cfg: config.Config{Paths: config.PathsConfig{SkillsCenterDir: root}}}

	first := buildSkillPackageZIP(t, "office-pack", "1.0.0", []testSkillPackageEntry{
		{ID: "word-helper", Version: "1.0.0", Present: true},
		{ID: "excel-helper", Version: "1.0.0", Present: true},
	})
	mutation, record, err := registry.BeginImportEditableSkillPackageArchive("office-pack", "1.0.0", bytes.NewReader(first), int64(len(first)))
	if err != nil {
		t.Fatalf("begin package install: %v", err)
	}
	if err := mutation.Commit(); err != nil {
		t.Fatalf("commit package install: %v", err)
	}
	if record.ID != "office-pack" || len(record.Skills) != 2 || record.SHA256 == "" {
		t.Fatalf("unexpected package record: %#v", record)
	}
	for _, id := range []string{"word-helper", "excel-helper"} {
		if _, err := os.Stat(filepath.Join(root, id, "SKILL.md")); err != nil {
			t.Fatalf("installed child %s: %v", id, err)
		}
	}
	recordPath := filepath.Join(root, ".package", "office-pack.json")
	assertSkillPackageRecord(t, recordPath, "office-pack", "1.0.0", []string{"excel-helper", "word-helper"})
	packages, err := registry.EditableSkillPackages()
	if err != nil || len(packages) != 1 || packages[0].ID != "office-pack" {
		t.Fatalf("unexpected package list: %#v err=%v", packages, err)
	}
	assertNoPackageArchives(t, root)
	if err := registry.DeleteEditableSkill("word-helper"); !errors.Is(err, ErrSkillPackageConflict) {
		t.Fatalf("expected package ownership conflict, got %v", err)
	}

	updated := buildSkillPackageZIP(t, "office-pack", "2.0.0", []testSkillPackageEntry{
		{ID: "word-helper", Version: "2.0.0", Present: true},
		{ID: "slides-helper", Version: "1.0.0", Present: true},
	})
	updateMutation, _, err := registry.BeginImportEditableSkillPackageArchive("office-pack", "2.0.0", bytes.NewReader(updated), int64(len(updated)))
	if err != nil {
		t.Fatalf("begin package update: %v", err)
	}
	if err := updateMutation.Rollback(); err != nil {
		t.Fatalf("rollback package update: %v", err)
	}
	assertSkillPackageRecord(t, recordPath, "office-pack", "1.0.0", []string{"excel-helper", "word-helper"})
	if _, err := os.Stat(filepath.Join(root, "excel-helper", "SKILL.md")); err != nil {
		t.Fatalf("rollback did not restore old child: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "slides-helper")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rollback left new child: %v", err)
	}
	childMutation, childRecord, packageDeleted, err := registry.BeginDeleteEditableSkillPackageSkill("office-pack", "word-helper")
	if err != nil {
		t.Fatalf("begin package child delete: %v", err)
	}
	if packageDeleted || len(childRecord.Skills) != 1 || childRecord.Skills[0].ID != "excel-helper" {
		t.Fatalf("unexpected package child delete state: deleted=%v record=%#v", packageDeleted, childRecord)
	}
	if err := childMutation.Commit(); err != nil {
		t.Fatalf("commit package child delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "word-helper")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted package child remains: %v", err)
	}
	assertSkillPackageRecord(t, recordPath, "office-pack", "1.0.0", []string{"excel-helper"})

	deleteMutation, deleted, err := registry.BeginDeleteEditableSkillPackage("office-pack")
	if err != nil {
		t.Fatalf("begin package delete: %v", err)
	}
	if len(deleted.Skills) != 1 {
		t.Fatalf("unexpected deleted package record: %#v", deleted)
	}
	if err := deleteMutation.Commit(); err != nil {
		t.Fatalf("commit package delete: %v", err)
	}
	if _, err := os.Stat(recordPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("package record remains after delete: %v", err)
	}
	for _, id := range []string{"excel-helper"} {
		if _, err := os.Stat(filepath.Join(root, id)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("package child %s remains after delete: %v", id, err)
		}
	}
	packages, err = registry.EditableSkillPackages()
	if err != nil || len(packages) != 0 {
		t.Fatalf("package list not cleared: %#v err=%v", packages, err)
	}
}

func TestEditableSkillPackageAdoptsStandaloneSkillAndRestoresItOnRollback(t *testing.T) {
	root := t.TempDir()
	registry := &FileRegistry{cfg: config.Config{Paths: config.PathsConfig{SkillsCenterDir: root}}}
	standaloneRoot := filepath.Join(root, "word-helper")
	if err := os.MkdirAll(standaloneRoot, 0o755); err != nil {
		t.Fatalf("mkdir standalone skill: %v", err)
	}
	standaloneContent := []byte("---\nname: word-helper\ndescription: Standalone skill\nmetadata:\n  version: 0.9.0\n---\n\nStandalone content.\n")
	standalonePath := filepath.Join(standaloneRoot, "SKILL.md")
	if err := os.WriteFile(standalonePath, standaloneContent, 0o644); err != nil {
		t.Fatalf("write standalone skill: %v", err)
	}

	archive := buildSkillPackageZIP(t, "office-pack", "1.0.0", []testSkillPackageEntry{
		{ID: "word-helper", Version: "1.0.0", Present: true},
		{ID: "excel-helper", Version: "1.0.0", Present: true},
	})
	mutation, record, err := registry.BeginImportEditableSkillPackageArchive("office-pack", "1.0.0", bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("begin package install over standalone skill: %v", err)
	}
	if record.ID != "office-pack" || len(record.Skills) != 2 {
		t.Fatalf("unexpected adopted package record: %#v", record)
	}
	installedContent, err := os.ReadFile(standalonePath)
	if err != nil {
		t.Fatalf("read package-owned replacement: %v", err)
	}
	if bytes.Equal(installedContent, standaloneContent) {
		t.Fatal("standalone skill was not replaced by package content")
	}
	if err := mutation.Rollback(); err != nil {
		t.Fatalf("rollback package adoption: %v", err)
	}
	restoredContent, err := os.ReadFile(standalonePath)
	if err != nil {
		t.Fatalf("read restored standalone skill: %v", err)
	}
	if !bytes.Equal(restoredContent, standaloneContent) {
		t.Fatalf("rollback did not restore standalone skill: %q", restoredContent)
	}
	if _, err := os.Stat(filepath.Join(root, "excel-helper")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rollback left new package child: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".package", "office-pack.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rollback left package state: %v", err)
	}
}

func TestEditableSkillPackageRejectsMissingRequiredSkillWithoutResidue(t *testing.T) {
	root := t.TempDir()
	registry := &FileRegistry{cfg: config.Config{Paths: config.PathsConfig{SkillsCenterDir: root}}}
	archive := buildSkillPackageZIP(t, "office-pack", "1.0.0", []testSkillPackageEntry{
		{ID: "word-helper", Version: "1.0.0", Present: false},
	})
	if _, _, err := registry.BeginImportEditableSkillPackageArchive("office-pack", "1.0.0", bytes.NewReader(archive), int64(len(archive))); err == nil {
		t.Fatal("expected missing required child rejection")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read skills root: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() != ".package" {
			t.Fatalf("unexpected residue after rejected package: %s", entry.Name())
		}
	}
}

func TestEditableSkillPackageDeletingLastChildRemovesPackageState(t *testing.T) {
	root := t.TempDir()
	registry := &FileRegistry{cfg: config.Config{Paths: config.PathsConfig{SkillsCenterDir: root}}}
	archive := buildSkillPackageZIP(t, "single-pack", "1.0.0", []testSkillPackageEntry{
		{ID: "only-skill", Version: "1.0.0", Present: true},
	})
	installMutation, _, err := registry.BeginImportEditableSkillPackageArchive("single-pack", "1.0.0", bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("begin package install: %v", err)
	}
	if err := installMutation.Commit(); err != nil {
		t.Fatalf("commit package install: %v", err)
	}
	deleteMutation, record, packageDeleted, err := registry.BeginDeleteEditableSkillPackageSkill("single-pack", "only-skill")
	if err != nil {
		t.Fatalf("begin last child delete: %v", err)
	}
	if !packageDeleted || len(record.Skills) != 0 {
		t.Fatalf("expected empty deleted package, got deleted=%v record=%#v", packageDeleted, record)
	}
	if err := deleteMutation.Commit(); err != nil {
		t.Fatalf("commit last child delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".package", "single-pack.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("package state remains after last child delete: %v", err)
	}
}

func TestEditableSkillPackageStateDirectoryIsNotSkillCatalogContent(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".package"), 0o755); err != nil {
		t.Fatalf("mkdir package state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".package", "office-pack.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write package state: %v", err)
	}
	registry := &FileRegistry{cfg: config.Config{Paths: config.PathsConfig{SkillsCenterDir: root}}}
	items, err := registry.AdminSkills()
	if err != nil {
		t.Fatalf("list admin skills: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("package state leaked into skill catalog: %#v", items)
	}
}

func buildSkillPackageZIP(t *testing.T, packageID string, version string, entries []testSkillPackageEntry) []byte {
	t.Helper()
	manifest := skillPackageManifest{SchemaVersion: 1, Type: "skill-package", ID: packageID, Version: version}
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, entry := range entries {
		manifest.Skills = append(manifest.Skills, skillPackageManifestSkill{
			ID: entry.ID, Version: entry.Version, Path: "skills/" + entry.ID + "/", Optional: entry.Optional,
		})
		if !entry.Present {
			continue
		}
		file, err := writer.Create("skills/" + entry.ID + "/SKILL.md")
		if err != nil {
			t.Fatalf("create child skill: %v", err)
		}
		if _, err := file.Write([]byte("---\nname: " + entry.ID + "\ndescription: Package child\nmetadata:\n  version: " + entry.Version + "\n---\n\nUse this skill.\n")); err != nil {
			t.Fatalf("write child skill: %v", err)
		}
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	manifestFile, err := writer.Create("manifest.json")
	if err != nil {
		t.Fatalf("create manifest: %v", err)
	}
	if _, err := manifestFile.Write(encoded); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close package ZIP: %v", err)
	}
	return output.Bytes()
}

func assertSkillPackageRecord(t *testing.T, path string, packageID string, version string, skillIDs []string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read package record: %v", err)
	}
	var record SkillPackageRecord
	if err := json.Unmarshal(content, &record); err != nil {
		t.Fatalf("decode package record: %v", err)
	}
	if record.ID != packageID || record.Version != version {
		t.Fatalf("unexpected package identity: %#v", record)
	}
	actual := make([]string, 0, len(record.Skills))
	for _, skill := range record.Skills {
		actual = append(actual, skill.ID)
	}
	if len(actual) != len(skillIDs) {
		t.Fatalf("unexpected package children: %#v", actual)
	}
	for index := range actual {
		if actual[index] != skillIDs[index] {
			t.Fatalf("unexpected package children: %#v", actual)
		}
	}
}

func assertNoPackageArchives(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".zip" {
			t.Fatalf("package ZIP was retained: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan package files: %v", err)
	}
}
