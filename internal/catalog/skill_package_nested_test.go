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

func nestedPackageZIP(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for path, content := range files {
		f, e := w.Create(path)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.Write([]byte(content)); e != nil {
			t.Fatal(e)
		}
	}
	if e := w.Close(); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func TestNestedPackageMinimalManifestAndDeclaredMembers(t *testing.T) {
	for _, prefix := range []string{"", "wrapper/"} {
		t.Run(prefix, func(t *testing.T) {
			root := t.TempDir()
			r := &FileRegistry{cfg: config.Config{Paths: config.PathsConfig{SkillsCenterDir: root}}}
			data := nestedPackageZIP(t, map[string]string{prefix + "package.json": `{"name":"suite","skills":[{"key":"calendar"}]}`, prefix + "calendar/SKILL.md": "---\nname: calendar\ndisplayName: Calendar\n---\nCalendar instructions", prefix + "docs/readme.md": "support file", prefix + "docs/subskill/SKILL.md": "Not a direct child"})
			id, version, isPackage, e := DetectSkillPackageArchive(bytes.NewReader(data), int64(len(data)))
			if e != nil || !isPackage || id != "suite" || version != "" {
				t.Fatalf("detect %s %s %v %v", id, version, isPackage, e)
			}
			mutation, record, e := r.BeginImportEditableSkillPackageArchive(id, version, bytes.NewReader(data), int64(len(data)))
			if e != nil {
				t.Fatal(e)
			}
			if e := mutation.Commit(); e != nil {
				t.Fatal(e)
			}
			if len(record.Skills) != 1 || record.Skills[0].ID != "suite/calendar" || record.Skills[0].DisplayName != "Calendar" {
				t.Fatalf("members %#v", record)
			}
			raw, e := os.ReadFile(filepath.Join(root, "suite", "package.json"))
			if e != nil {
				t.Fatal(e)
			}
			var m map[string]any
			if e := json.Unmarshal(raw, &m); e != nil {
				t.Fatal(e)
			}
			if got := m["skills"].([]any); len(got) != 1 || got[0].(map[string]any)["key"] != "calendar" {
				t.Fatalf("members=%v", got)
			}
			if len(m) != 2 {
				t.Fatalf("manufactured metadata %s", raw)
			}
		})
	}
}
func TestNestedPackageUpdateIsolationAndRollback(t *testing.T) {
	root := t.TempDir()
	r := &FileRegistry{cfg: config.Config{Paths: config.PathsConfig{SkillsCenterDir: root}}}
	if e := os.MkdirAll(filepath.Join(root, "same"), 0o755); e != nil {
		t.Fatal(e)
	}
	outside := filepath.Join(root, "same", "SKILL.md")
	if e := os.WriteFile(outside, []byte("standalone"), 0o644); e != nil {
		t.Fatal(e)
	}
	install := func(content string) *EditableSkillPackageMutation {
		t.Helper()
		data := nestedPackageZIP(t, map[string]string{"package.json": `{"name":"suite","skills":[{"key":"same"}]}`, "same/SKILL.md": "---\nname: same\n---\n" + content})
		m, _, e := r.BeginImportEditableSkillPackageArchive("suite", "", bytes.NewReader(data), int64(len(data)))
		if e != nil {
			t.Fatal(e)
		}
		return m
	}
	if e := install("old").Commit(); e != nil {
		t.Fatal(e)
	}
	if e := install("new").Rollback(); e != nil {
		t.Fatal(e)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "suite", "same", "SKILL.md"))
	if !bytes.Contains(raw, []byte("old")) {
		t.Fatal("rollback lost old member")
	}
	raw, _ = os.ReadFile(outside)
	if string(raw) != "standalone" {
		t.Fatal("package mutation touched standalone")
	}
	if e := install("new").Commit(); e != nil {
		t.Fatal(e)
	}
	raw, _ = os.ReadFile(outside)
	if string(raw) != "standalone" {
		t.Fatal("package update touched standalone")
	}
	if e := os.WriteFile(outside, []byte("standalone updated"), 0o644); e != nil {
		t.Fatal(e)
	}
	raw, _ = os.ReadFile(filepath.Join(root, "suite", "same", "SKILL.md"))
	if !bytes.Contains(raw, []byte("new")) {
		t.Fatal("standalone edit touched package")
	}
}
func TestNestedPackageManifestEditingCASAndRollback(t *testing.T) {
	root := t.TempDir()
	r := &FileRegistry{cfg: config.Config{Paths: config.PathsConfig{SkillsCenterDir: root}}}
	if e := os.Mkdir(filepath.Join(root, "suite"), 0o755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(root, "suite", "package.json"), []byte(`{"name":"suite","skills":[]}`), 0o644); e != nil {
		t.Fatal(e)
	}
	old, e := r.ReadEditableSkillPackageManifest("suite")
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e := r.BeginUpdateEditableSkillPackageManifest("suite", `{"name":"suite","skills":[]}`, "stale"); !errors.Is(e, ErrSkillConflict) {
		t.Fatalf("stale accepted %v", e)
	}
	m, record, e := r.BeginUpdateEditableSkillPackageManifest("suite", `{"name":"suite","skills":[],"displayName":"Suite","metadata":{"revision":"r1"}}`, old.SHA256)
	if e != nil {
		t.Fatal(e)
	}
	if record.Presentation.DisplayName != "Suite" || record.Presentation.Revision != "r1" {
		t.Fatalf("metadata missing %#v", record)
	}
	if _, e := r.ReadEditableSkillPackageManifest("suite"); e != nil {
		t.Fatal(e)
	} // no mutex reentry
	if e := m.Rollback(); e != nil {
		t.Fatal(e)
	}
	current, e := r.ReadEditableSkillPackageManifest("suite")
	if e != nil || current.Content != old.Content {
		t.Fatalf("rollback failed %v", e)
	}
}
func TestNestedPackageMigrationPreservesIndependentCopyAndRecovery(t *testing.T) {
	root := t.TempDir()
	r := &FileRegistry{cfg: config.Config{Paths: config.PathsConfig{SkillsCenterDir: root}}}
	if e := os.MkdirAll(filepath.Join(root, "same"), 0o755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(root, "same", "SKILL.md"), []byte("---\nname: same\n---\nbody"), 0o644); e != nil {
		t.Fatal(e)
	}
	legacy := SkillPackageRecord{ID: "suite", Name: "Suite", SchemaVersion: 1, Version: "1", SHA256: "digest", InstalledAt: 1, Skills: []SkillPackageRecordSkill{{ID: "same", Version: "1"}, {ID: "missing", Version: "1"}}}
	raw, _ := json.Marshal(legacy)
	path, e := skillPackageRecordPath(root, "suite")
	if e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, raw, 0o644); e != nil {
		t.Fatal(e)
	}
	m, e := r.BeginMigrateLegacySkillPackage("suite")
	if e != nil {
		t.Fatal(e)
	}
	if e := m.Rollback(); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(path); e != nil {
		t.Fatal("legacy record not restored")
	}
	backups, e := r.MigrateLegacySkillPackages()
	if e != nil || len(backups) != 1 {
		t.Fatalf("migration %v %v", backups, e)
	}
	for _, path := range []string{filepath.Join(root, "same", "SKILL.md"), filepath.Join(root, "suite", "same", "SKILL.md"), filepath.Join(backups[0], ".package", "suite.json")} {
		if _, e := os.Stat(path); e != nil {
			t.Fatal(e)
		}
	}
	record, e := ScanSkillPackageRecord(root, "suite")
	if e != nil || len(record.Skills) != 2 || len(record.Skills[1].Diagnostics) == 0 {
		t.Fatalf("missing declared member placeholder %#v %v", record, e)
	}
	again, e := r.MigrateLegacySkillPackages()
	if e != nil || len(again) != 0 {
		t.Fatalf("not idempotent %v %v", again, e)
	}
}
func TestNestedPackageRejectsInvalidIdentityAndSymlinks(t *testing.T) {
	for _, content := range []string{`{}`, `{"name":"a/b"}`, `{"name":".."}`, `{"name":null}`, `{"name":"/absolute"}`} {
		if _, e := parseSkillPackageMetadata([]byte(content)); e == nil {
			t.Fatalf("accepted %s", content)
		}
	}
	root := t.TempDir()
	outside := t.TempDir()
	if e := os.WriteFile(filepath.Join(outside, "package.json"), []byte(`{"name":"suite","skills":[]}`), 0o644); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(outside, filepath.Join(root, "suite")); e != nil {
		t.Skip(e)
	}
	if _, e := ScanSkillPackageRecord(root, "suite"); !errors.Is(e, ErrSkillSymlink) {
		t.Fatalf("symlink accepted: %v", e)
	}
}

func TestNestedPackageImportPreservesRootIcon(t *testing.T) {
	root := t.TempDir()
	r := &FileRegistry{cfg: config.Config{Paths: config.PathsConfig{SkillsCenterDir: root}}}
	svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M0 0h24v24H0z"/></svg>`
	data := nestedPackageZIP(t, map[string]string{"package.json": `{"name":"suite","skills":[]}`, "icon.svg": svg})
	mutation, record, err := r.BeginImportEditableSkillPackageArchive("suite", "", bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer mutation.Rollback()
	if err := mutation.Commit(); err != nil {
		t.Fatal(err)
	}
	if !record.HasIcon {
		t.Fatal("import response is missing icon")
	}
	icon, mediaType, err := r.ReadSkillPackageIcon("suite")
	if err != nil || mediaType != "image/svg+xml" || string(icon) != svg {
		t.Fatalf("imported icon %q %q %v", icon, mediaType, err)
	}
}
