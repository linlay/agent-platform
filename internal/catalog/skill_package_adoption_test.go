package catalog

import (
	"agent-platform/internal/config"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func prepareAdoptionTest(t *testing.T) (*FileRegistry, *PreparedEditableSkillPackage, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "skills")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	r := &FileRegistry{cfg: config.Config{Paths: config.PathsConfig{SkillsCenterDir: root}}}
	archive := buildSkillPackageZIP(t, "pack", "1.0.0", []testSkillPackageEntry{{ID: "test-skill", Version: "1.0.0", Present: true}, {ID: "extra-skill", Version: "1.0.0", Present: true}})
	p, err := r.PrepareEditableSkillPackageArchive("pack", "1.0.0", bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	dst := filepath.Join(root, "test-skill")
	if err := os.MkdirAll(dst, 0755); err != nil {
		t.Fatal(err)
	}
	var src string
	for _, s := range p.skills {
		if s.ID == "test-skill" {
			src = s.Root
		}
	}
	body, err := os.ReadFile(filepath.Join(src, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dst, "SKILL.md"), body, 0644); err != nil {
		t.Fatal(err)
	}
	return r, p, dst
}

func TestSkillPackageReplacesStandaloneAndRetainsBackup(t *testing.T) {
	for _, body := range []string{"", "---\nname: test-skill\ndescription: newer standalone\nmetadata:\n  version: 99.0.0\n---\nnewer local instructions\n"} {
		t.Run(body, func(t *testing.T) {
			_, p, dst := prepareAdoptionTest(t)
			if body != "" {
				if err := os.WriteFile(filepath.Join(dst, "SKILL.md"), []byte(body), 0644); err != nil {
					t.Fatal(err)
				}
			}
			old, err := os.ReadFile(filepath.Join(dst, "SKILL.md"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dst, "skill.json"), []byte(`{"custom":true,"version":"99.0.0"}`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dst, ".local-script"), []byte("custom executable"), 0755); err != nil {
				t.Fatal(err)
			}
			m, _, err := p.Begin()
			if err != nil {
				t.Fatal(err)
			}
			backup := m.BackupPath()
			if backup == "" {
				t.Fatal("no backup")
			}
			if err := m.Commit(); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(backup, "test-skill", "SKILL.md"))
			if err != nil || !bytes.Equal(old, got) {
				t.Fatalf("backup %q %v", got, err)
			}
			if _, err := os.Stat(filepath.Join(backup, "test-skill", "skill.json")); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(filepath.Join(backup, "test-skill", ".local-script"))
			if err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS != "windows" && info.Mode().Perm() != 0755 {
				t.Fatalf("executable mode lost: %v", info.Mode())
			}
			if data, err := os.ReadFile(filepath.Join(backup, "test-skill", ".local-script")); err != nil || string(data) != "custom executable" {
				t.Fatal("hidden file lost")
			}
			if _, err := os.Stat(filepath.Join(dst, "skill.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("old metadata still present")
			}
			got, err = os.ReadFile(filepath.Join(dst, "SKILL.md"))
			if err != nil || !bytes.Contains(got, []byte("name: test-skill")) {
				t.Fatalf("incoming not published %s %v", got, err)
			}
			owners, err := readSkillPackageOwners(filepath.Dir(dst))
			if err != nil || owners["test-skill"] != "pack" {
				t.Fatalf("owners %v %v", owners, err)
			}
		})
	}
}
func TestSkillPackageReplacementRollbackRestoresStandalone(t *testing.T) {
	_, p, dst := prepareAdoptionTest(t)
	if err := os.WriteFile(filepath.Join(dst, "custom"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	m, _, err := p.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Rollback(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(dst, "custom")); err != nil || string(data) != "preserve" {
		t.Fatalf("rollback %s %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dst), "extra-skill")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("new skill residue")
	}
	owners, err := readSkillPackageOwners(filepath.Dir(dst))
	if err != nil || owners["test-skill"] != "" {
		t.Fatal("ownership not restored")
	}
}
func TestSkillPackageReplacementRejectsSymlinkAndCaseCollision(t *testing.T) {
	for _, kind := range []string{"root-link", "nested-link", "case"} {
		t.Run(kind, func(t *testing.T) {
			_, p, dst := prepareAdoptionTest(t)
			switch kind {
			case "root-link":
				if err := os.RemoveAll(dst); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), dst); err != nil {
					t.Skip(err)
				}
			case "nested-link":
				if err := os.Symlink(t.TempDir(), filepath.Join(dst, "linked")); err != nil {
					t.Skip(err)
				}
			case "case":
				if err := os.Rename(dst, filepath.Join(filepath.Dir(dst), "Test-Skill")); err != nil {
					t.Fatal(err)
				}
			}
			m, _, err := p.Begin()
			if err == nil {
				m.Rollback()
				t.Fatal("unsafe target accepted")
			}
			if kind != "case" && !errors.Is(err, ErrSkillSymlink) {
				t.Fatal(err)
			}
			if kind == "case" && !errors.Is(err, ErrSkillPackageConflict) {
				t.Fatal(err)
			}
		})
	}
}
func TestSkillPackageUpdateRetainsOldMembersAndRecord(t *testing.T) {
	r, p, dst := prepareAdoptionTest(t)
	m, _, err := p.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Commit(); err != nil {
		t.Fatal(err)
	}
	archive := buildSkillPackageZIP(t, "pack", "2.0.0", []testSkillPackageEntry{{ID: "test-skill", Version: "2.0.0", Present: true}})
	next, err := r.PrepareEditableSkillPackageArchive("pack", "2.0.0", bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	m, _, err = next.Begin()
	if err != nil {
		t.Fatal(err)
	}
	backup := m.BackupPath()
	if err := m.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"test-skill/SKILL.md", "extra-skill/SKILL.md", ".original-package-record.json"} {
		if _, err := os.Stat(filepath.Join(backup, name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dst), "extra-skill")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("removed member retained")
	}
}
func TestSkillPackageAdoptionCannotTakeAnotherPackagesSkill(t *testing.T) {
	r, p, dst := prepareAdoptionTest(t)
	other := SkillPackageRecord{SchemaVersion: 1, ID: "other", Version: "1.0.0", SHA256: "test-hash", InstalledAt: 1, Skills: []SkillPackageRecordSkill{{ID: "test-skill", Version: "1.0.0"}}}
	encoded, err := json.Marshal(other)
	if err != nil {
		t.Fatal(err)
	}
	recordPath, err := skillPackageRecordPath(filepath.Dir(dst), "other")
	if err != nil {
		t.Fatal(err)
	}
	if err = writeSkillPackageRecordFile(recordPath, encoded); err != nil {
		t.Fatal(err)
	}
	_, _, err = p.Begin()
	if !errors.Is(err, ErrSkillPackageConflict) {
		t.Fatalf("other owner accepted %v", err)
	}
	packages, err := r.EditableSkillPackages()
	if err != nil || len(packages) != 1 || packages[0].ID != "other" {
		t.Fatalf("ownership changed %v %v", packages, err)
	}
}
