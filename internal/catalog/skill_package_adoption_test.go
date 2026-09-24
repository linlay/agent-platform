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
func TestSkillPackageAdoptsIdenticalStandaloneAndPreservesMetadata(t *testing.T) {
	_, p, dst := prepareAdoptionTest(t)
	metadata := []byte(`{"id":"test-skill","name":"Display","version":"1.0.0","description":"desc","tags":[]}`)
	if err := os.WriteFile(filepath.Join(dst, "skill.json"), metadata, 0600); err != nil {
		t.Fatal(err)
	}
	m, _, err := p.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Commit(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "skill.json"))
	if err != nil || !bytes.Equal(got, metadata) {
		t.Fatalf("metadata lost: %s %v", got, err)
	}
	owners, err := readSkillPackageOwners(filepath.Dir(dst))
	if err != nil || owners["test-skill"] != "pack" {
		t.Fatalf("owners %v %v", owners, err)
	}
}
func TestSkillPackageAdoptionProtectsChangesAndBacksUp(t *testing.T) {
	_, p, dst := prepareAdoptionTest(t)
	old := []byte("local instructions")
	if err := os.WriteFile(filepath.Join(dst, "local.txt"), old, 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err := p.Begin()
	var conflict *SkillPackageAdoptionConflict
	if !errors.As(err, &conflict) || !errors.Is(err, ErrSkillPackageConflict) || len(conflict.Skills) != 1 || conflict.Skills[0].ChangedPaths[0] != "local.txt" {
		t.Fatalf("conflict %v", err)
	}
	approval := &SkillPackageAdoptionApproval{ArchiveSHA256: conflict.ArchiveSHA256, ExpectedRevisions: map[string]string{"test-skill": conflict.Skills[0].Revision}}
	wrong := *approval
	wrong.ArchiveSHA256 = "changed"
	if _, _, err = p.BeginWithAdoptions(&wrong); err == nil {
		t.Fatal("accepted new ZIP")
	}
	if err := os.WriteFile(filepath.Join(dst, "local.txt"), []byte("concurrent edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = p.BeginWithAdoptions(approval); !errors.As(err, &conflict) {
		t.Fatalf("accepted stale revision %v", err)
	}
	approval.ExpectedRevisions["test-skill"] = conflict.Skills[0].Revision
	m, _, err := p.BeginWithAdoptions(approval)
	if err != nil {
		t.Fatal(err)
	}
	backup := m.BackupPath()
	if backup == "" {
		t.Fatal("no backup")
	}
	if err = m.Commit(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(backup, "test-skill", "local.txt"))
	if err != nil || string(data) != "concurrent edit" {
		t.Fatalf("backup %q %v", data, err)
	}
	if _, err = os.Stat(filepath.Join(dst, "local.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("replacement not published")
	}
}
func TestSkillPackageAdoptionRollbackRestoresStandalone(t *testing.T) {
	_, p, dst := prepareAdoptionTest(t)
	os.WriteFile(filepath.Join(dst, "custom"), []byte("preserve"), 0600)
	_, _, err := p.Begin()
	var c *SkillPackageAdoptionConflict
	if !errors.As(err, &c) {
		t.Fatal(err)
	}
	m, _, err := p.BeginWithAdoptions(&SkillPackageAdoptionApproval{ArchiveSHA256: c.ArchiveSHA256, ExpectedRevisions: map[string]string{"test-skill": c.Skills[0].Revision}})
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Rollback(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(dst, "custom")); err != nil || string(data) != "preserve" {
		t.Fatalf("rollback %s %v", data, err)
	}
	if _, err = os.Stat(filepath.Join(filepath.Dir(dst), "extra-skill")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("new skill residue")
	}
	owners, err := readSkillPackageOwners(filepath.Dir(dst))
	if err != nil || owners["test-skill"] != "" {
		t.Fatal("ownership not restored")
	}
}
func TestSkillPackageAdoptionRejectsSymlinkAndCustomMetadata(t *testing.T) {
	for _, kind := range []string{"symlink", "metadata", "executable"} {
		t.Run(kind, func(t *testing.T) {
			_, p, dst := prepareAdoptionTest(t)
			switch kind {
			case "symlink":
				if err := os.Symlink(t.TempDir(), filepath.Join(dst, "linked")); err != nil {
					t.Skip(err)
				}
			case "metadata":
				os.WriteFile(filepath.Join(dst, "skill.json"), []byte(`{"custom":true}`), 0600)
			case "executable":
				if runtime.GOOS == "windows" {
					t.Skip("Windows does not represent POSIX executable mode")
				}
				os.Chmod(filepath.Join(dst, "SKILL.md"), 0755)
			}
			_, _, err := p.Begin()
			if err == nil {
				t.Fatal("unconfirmed mutation")
			}
			if kind == "symlink" && !errors.Is(err, ErrSkillSymlink) {
				t.Fatal(err)
			}
		})
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

func TestSkillPackageAdoptionMetadataEditInvalidatesConfirmation(t *testing.T) {
	_, p, dst := prepareAdoptionTest(t)
	os.WriteFile(filepath.Join(dst, "local"), []byte("custom"), 0600)
	os.WriteFile(filepath.Join(dst, "skill.json"), []byte(`{"id":"test-skill","name":"Old","version":"1","description":"","tags":[]}`), 0600)
	_, _, err := p.Begin()
	var conflict *SkillPackageAdoptionConflict
	if !errors.As(err, &conflict) {
		t.Fatal(err)
	}
	approval := &SkillPackageAdoptionApproval{ArchiveSHA256: conflict.ArchiveSHA256, ExpectedRevisions: map[string]string{"test-skill": conflict.Skills[0].Revision}}
	os.WriteFile(filepath.Join(dst, "skill.json"), []byte(`{"id":"test-skill","name":"New","version":"1","description":"","tags":[]}`), 0600)
	if _, _, err = p.BeginWithAdoptions(approval); !errors.As(err, &conflict) {
		t.Fatalf("metadata changes lost %v", err)
	}
}
