package catalog

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/config"
)

func TestImplicitPackageMigrationIsExplicitAndBackedUp(t *testing.T) {
	root := t.TempDir()
	original := `{"name":"suite","custom":{"preserve":true}}`
	writeRuntimeAssemblerFile(t, filepath.Join(root, "suite", "package.json"), original)
	writeRuntimeAssemblerFile(t, filepath.Join(root, "suite", "member", "SKILL.md"), "# member")
	writeRuntimeAssemblerFile(t, filepath.Join(root, "suite", "docs", "readme.md"), "support")
	if _, err := ReadSkillPackageManifest(filepath.Join(root, "suite")); err == nil {
		t.Fatal("strict read accepted implicit package")
	}
	r := &FileRegistry{cfg: config.Config{Paths: config.PathsConfig{SkillsCenterDir: root}}}
	backups, err := r.MigrateLegacySkillPackages()
	if err != nil || len(backups) != 1 {
		t.Fatal(backups, err)
	}
	raw, err := os.ReadFile(filepath.Join(backups[0], "suite.package.json"))
	if err != nil || string(raw) != original {
		t.Fatal("backup mismatch", err)
	}
	manifest, err := ReadSkillPackageManifest(filepath.Join(root, "suite"))
	if err != nil || len(manifest.Skills) != 1 || manifest.Skills[0].Key != "member" {
		t.Fatal(manifest, err)
	}
	upgraded, err := os.ReadFile(filepath.Join(root, "suite", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(upgraded, &fields); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(fields["custom"], []byte("true")) {
		t.Fatalf("migration lost extensions: %s", upgraded)
	}
	backups, err = r.MigrateLegacySkillPackages()
	if err != nil || len(backups) != 0 {
		t.Fatal("migration repeated", backups, err)
	}
}

func TestManifestRegisterMissingMemberThenCreate(t *testing.T) {
	root := t.TempDir()
	writeRuntimeAssemblerFile(t, filepath.Join(root, "suite", "package.json"), `{"name":"suite","skills":[]}`)
	r := &FileRegistry{cfg: config.Config{Paths: config.PathsConfig{SkillsCenterDir: root}}}
	current, err := r.ReadEditableSkillPackageManifest("suite")
	if err != nil {
		t.Fatal(err)
	}
	m, record, err := r.BeginUpdateEditableSkillPackageManifest("suite", `{"name":"suite","skills":[{"key":"member"}]}`, current.SHA256)
	if err != nil || len(record.Skills) != 1 || len(record.Skills[0].Diagnostics) == 0 {
		t.Fatalf("%+v %v", record, err)
	}
	if err = m.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = r.CreateEditableSkill("suite/member", "---\nname: member\n---\n# Member", nil); err != nil {
		t.Fatal(err)
	}
	record, err = ScanSkillPackageRecord(root, "suite")
	if err != nil || len(record.Skills) != 1 || len(record.Skills[0].Diagnostics) != 0 {
		t.Fatalf("%+v %v", record, err)
	}
}

func TestPackageManifestExtensionsSurviveImportSaveAndMemberDelete(t *testing.T) {
	root := t.TempDir()
	r := &FileRegistry{cfg: config.Config{Paths: config.PathsConfig{SkillsCenterDir: root}}}
	raw := `{"name":"suite","author":{"id":9007199254740993},"skills":[{"key":"first","custom":{"label":"keep","count":9007199254740993}},{"key":"second","custom":false}]}`
	archive := nestedPackageZIP(t, map[string]string{"package.json": raw, "first/SKILL.md": "# First", "second/SKILL.md": "# Second"})
	imported, _, err := r.BeginImportEditableSkillPackageArchive("suite", "", bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	if err = imported.Commit(); err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, "suite", "package.json"))
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err = json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		var members []map[string]json.RawMessage
		if err = json.Unmarshal(fields["skills"], &members); err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(fields["author"], []byte("9007199254740993")) || len(members) == 0 || !bytes.Contains(members[0]["custom"], []byte("9007199254740993")) {
			t.Fatalf("extensions lost: %s", data)
		}
	}
	check()
	current, err := r.ReadEditableSkillPackageManifest("suite")
	if err != nil {
		t.Fatal(err)
	}
	saved, _, err := r.BeginUpdateEditableSkillPackageManifest("suite", current.Content, current.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if err = saved.Commit(); err != nil {
		t.Fatal(err)
	}
	check()
	removed, _, _, err := r.BeginDeleteEditableSkillPackageSkill("suite", "second")
	if err != nil {
		t.Fatal(err)
	}
	if err = removed.Commit(); err != nil {
		t.Fatal(err)
	}
	check()
}

func TestManifestEditRejectsUnsafeMemberButCanRemoveItsDeclaration(t *testing.T) {
	r, root := nestedSkillFixture(t)
	if err := os.RemoveAll(filepath.Join(root, "suite", "demo")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "demo"), filepath.Join(root, "suite", "demo")); err != nil {
		t.Skip(err)
	}
	current, err := r.ReadEditableSkillPackageManifest("suite")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.BeginUpdateEditableSkillPackageManifest("suite", current.Content, current.SHA256); err == nil {
		t.Fatal("unsafe member accepted")
	}
	m, record, err := r.BeginUpdateEditableSkillPackageManifest("suite", `{"name":"suite","skills":[{"key":"other"}]}`, current.SHA256)
	if err != nil || len(record.Skills) != 1 {
		t.Fatalf("cannot repair membership: %+v %v", record, err)
	}
	if err := m.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "suite", "demo")); err != nil {
		t.Fatal("manifest edit deleted content", err)
	}
}
