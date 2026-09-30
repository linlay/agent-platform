package catalog

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSkillPackageMemberKeysValidation(t *testing.T) {
	for _, skills := range []string{`null`, `["pdf"]`, `[{}]`, `[{"name":"pdf"}]`, `[{"key":"../pdf"}]`, `[{"key":"suite/pdf"}]`, `[{"key":" pdf"}]`, `[{"key":"pdf"},{"key":"PDF"}]`, `[{"key":"a/b/c"}]`, `[null]`} {
		if _, err := parseSkillPackageMetadata([]byte(`{"name":"suite","skills":` + skills + `}`)); err == nil {
			t.Errorf("accepted %s", skills)
		}
	}
	if _, err := parseSkillPackageMetadata([]byte(`{"name":"suite"}`)); err == nil {
		t.Fatal("missing list accepted")
	}
}

func TestSkillPackageDeclaredOrderAndMissingMembers(t *testing.T) {
	r, root := nestedSkillFixture(t)
	manifest := `{"name":"suite","skills":[{"key":"other"},{"key":"missing"}]}`
	writeRuntimeAssemblerFile(t, filepath.Join(root, "suite", "package.json"), manifest)
	record, err := ScanSkillPackageRecord(root, "suite")
	if err != nil || len(record.Skills) != 2 || record.Skills[0].ID != "suite/other" || record.Skills[1].ID != "suite/missing" || len(record.Skills[1].Diagnostics) != 1 {
		t.Fatalf("record=%+v err=%v", record, err)
	}
	defs, err := loadSkills(root, 0)
	if err != nil || len(defs) != 2 {
		t.Fatalf("defs=%v err=%v", defs, err)
	}
	if _, found := defs["suite/demo"]; found {
		t.Fatal("unlisted directory loaded")
	}
	if _, found, err := ResolveSkillDefinition("", root, "suite/demo"); err == nil && found {
		t.Fatal("unlisted directory resolved directly")
	}
	if _, err := r.ReadEditableSkillFile("suite/demo", "SKILL.md"); err == nil {
		t.Fatal("unlisted member editable")
	}
	before, _ := os.ReadFile(filepath.Join(root, "suite", "package.json"))
	mutation, _, _, err := r.BeginDeleteEditableSkillPackageSkill("suite", "missing")
	if err != nil {
		t.Fatal(err)
	}
	if err := mutation.Rollback(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(root, "suite", "package.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("missing member deletion rollback changed manifest")
	}
}

func TestSkillPackageMemberDeletionRollsBackListAndDirectory(t *testing.T) {
	r, root := nestedSkillFixture(t)
	before, _ := os.ReadFile(filepath.Join(root, "suite", "package.json"))
	mutation, _, _, err := r.BeginDeleteEditableSkillPackageSkill("suite", "demo")
	if err != nil {
		t.Fatal(err)
	}
	m, err := ReadSkillPackageManifest(filepath.Join(root, "suite"))
	if err != nil || !reflect.DeepEqual(m.Skills, []SkillPackageMember{{Key: "other"}}) {
		t.Fatalf("manifest=%+v err=%v", m, err)
	}
	snapshot, err := mutation.SnapshotSkill("suite/demo")
	if err != nil || snapshot.Exists || snapshot.Revision != "missing" {
		t.Fatalf("deleted member snapshot=%+v err=%v", snapshot, err)
	}
	if err := mutation.Rollback(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(root, "suite", "package.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("rollback changed manifest")
	}
	if _, err := os.Stat(filepath.Join(root, "suite", "demo", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
}

func TestSkillPackageManifestCannotRemoveUsedMember(t *testing.T) {
	r, _ := nestedSkillFixture(t)
	r.agents = map[string]AgentDefinition{"writer": {Key: "writer", Skills: []string{"suite/demo"}}}
	before, err := r.ReadEditableSkillPackageManifest("suite")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.BeginUpdateEditableSkillPackageManifest("suite", `{"name":"suite","skills":[]}`, before.SHA256); !errors.Is(err, ErrSkillPackageConflict) {
		t.Fatalf("used member removal: %v", err)
	}
	after, _ := r.ReadEditableSkillPackageManifest("suite")
	if after.Content != before.Content {
		t.Fatal("rejected edit changed manifest")
	}
}

func TestSkillPackageImportRejectsMissingDeclaredMember(t *testing.T) {
	r, root := nestedSkillFixture(t)
	before, _ := os.ReadFile(filepath.Join(root, "suite", "package.json"))
	data := nestedPackageZIP(t, map[string]string{"package.json": `{"name":"suite","skills":[{"key":"missing"}]}`})
	if _, _, err := r.BeginImportEditableSkillPackageArchive("suite", "", bytes.NewReader(data), int64(len(data))); err == nil {
		t.Fatal("missing member imported")
	}
	after, _ := os.ReadFile(filepath.Join(root, "suite", "package.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("failed import changed package")
	}
}

func TestSkillPackageMemberSymlinkDoesNotLoadOrBlockOtherSkills(t *testing.T) {
	_, root := nestedSkillFixture(t)
	if err := os.RemoveAll(filepath.Join(root, "suite", "demo")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "demo"), filepath.Join(root, "suite", "demo")); err != nil {
		t.Skip(err)
	}
	defs, err := loadSkills(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := defs["suite/demo"]; ok {
		t.Fatal("symlink member loaded")
	}
	if len(defs) != 2 {
		t.Fatalf("unrelated skills lost: %v", defs)
	}
	record, err := ScanSkillPackageRecord(root, "suite")
	if err != nil || len(record.Skills) != 2 || len(record.Skills[0].Diagnostics) == 0 {
		t.Fatalf("package lost unsafe-member diagnostics: %+v %v", record, err)
	}
}
