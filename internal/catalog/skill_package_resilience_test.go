package catalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPackageScanIgnoresLegacyDirectoriesAndIsolatesBrokenManifest(t *testing.T) {
	r, root := nestedSkillFixture(t)
	for _, id := range []string{"sample.example", "builtin-dbx", "builtin-httpx"} {
		writeRuntimeAssemblerFile(t, filepath.Join(root, id, "SKILL.md"), "---\nname: "+id+"\n---\nold content")
	}
	for id, content := range map[string]string{"broken": "{bad json", "mismatch": `{"name":"different"}`} {
		writeRuntimeAssemblerFile(t, filepath.Join(root, id, "package.json"), content)
		writeRuntimeAssemblerFile(t, filepath.Join(root, id, "member", "SKILL.md"), "# Hidden invalid package member")
	}
	for attempt := 0; attempt < 2; attempt++ {
		packages, err := r.EditableSkillPackages()
		if err != nil || len(packages) != 1 || packages[0].ID != "suite" {
			t.Fatalf("packages=%+v err=%v", packages, err)
		}
		skills, err := r.AdminSkills()
		if err != nil || len(skills) != 3 {
			t.Fatalf("skills=%+v err=%v", skills, err)
		}
		defs, err := loadSkills(root, 0)
		if err != nil || len(defs) != 3 {
			t.Fatalf("defs=%+v err=%v", defs, err)
		}
		owners, err := readSkillPackageOwners(root)
		if err != nil || len(owners) != 2 {
			t.Fatalf("owners=%+v err=%v", owners, err)
		}
	}
}

func TestLegacyMigrationConflictPreservesOriginalAndContinues(t *testing.T) {
	r, root := nestedSkillFixture(t)
	originals := map[string][]byte{}
	for _, id := range []string{"demo", "safe-package"} {
		record := SkillPackageRecord{ID: id, SchemaVersion: 1, Version: "1", SHA256: "old-digest", InstalledAt: 1, Skills: []SkillPackageRecordSkill{{ID: "demo", Version: "1"}}}
		raw, _ := json.Marshal(record)
		path, err := skillPackageRecordPath(root, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0644); err != nil {
			t.Fatal(err)
		}
		originals[id] = raw
	}
	originalSkill, err := os.ReadFile(filepath.Join(root, "demo", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	backups, err := r.MigrateLegacySkillPackages()
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups=%v err=%v", backups, err)
	}
	data, _ := os.ReadFile(filepath.Join(root, ".package", "demo.json"))
	if string(data) != string(originals["demo"]) {
		t.Fatal("conflicting record changed")
	}
	data, _ = os.ReadFile(filepath.Join(root, "demo", "SKILL.md"))
	if string(data) != string(originalSkill) {
		t.Fatal("standalone changed")
	}
	if _, err := os.Stat(filepath.Join(root, "safe-package", "demo", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	again, err := r.MigrateLegacySkillPackages()
	if err != nil || len(again) != 0 {
		t.Fatalf("repeat=%v err=%v", again, err)
	}
}
