package catalog

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/config"
)

func nestedSkillFixture(t *testing.T) (*FileRegistry, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "skills-center")
	writeRuntimeAssemblerFile(t, filepath.Join(root, "suite", "package.json"), `{"name":"suite","skills":[{"key":"demo"},{"key":"other"}]}`)
	for key, body := range map[string]string{"demo": "Standalone", "suite/demo": "Packaged", "suite/other": "Other"} {
		writeRuntimeAssemblerFile(t, filepath.Join(root, filepath.FromSlash(key), "SKILL.md"), "---\nname: "+filepath.Base(key)+"\ndescription: "+body+"\nversion: 1.0.0\n---\n"+body+"\n")
	}
	return &FileRegistry{cfg: config.Config{Paths: config.PathsConfig{SkillsCenterDir: root}}}, root
}

func TestNestedSkillsCatalogAndEditorIsolation(t *testing.T) {
	r, root := nestedSkillFixture(t)
	writeRuntimeAssemblerFile(t, filepath.Join(root, "demo", "sub-skills", "hidden", "SKILL.md"), "---\nname: hidden\ndescription: hidden\n---\nbody")
	defs, err := loadSkills(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 3 || defs["demo"].Description != "Standalone" || defs["suite/demo"].Description != "Packaged" {
		t.Fatalf("catalog=%#v", defs)
	}
	items, err := r.AdminSkills()
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, item := range items {
		keys = append(keys, item.ID)
		for _, d := range item.Diagnostics {
			if d.Code == "skill_name_id_mismatch" {
				t.Fatalf("nested basename misdiagnosed: %+v", d)
			}
		}
	}
	if !reflect.DeepEqual(keys, []string{"demo", "suite/demo", "suite/other"}) {
		t.Fatalf("keys=%v", keys)
	}
	file, err := r.ReadEditableSkillFile("suite/demo", "SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(file.Content, "Packaged", "Edited", -1)
	if _, err := r.WriteEditableSkillFile("suite/demo", "SKILL.md", updated, "utf-8", file.SHA256); err != nil {
		t.Fatal(err)
	}
	standalone, err := r.ReadEditableSkillFile("demo", "SKILL.md")
	if err != nil || !strings.Contains(standalone.Content, "Standalone") {
		t.Fatalf("standalone changed %v", err)
	}
	snapshot, err := r.SnapshotEditableSkill("suite/demo")
	if err != nil || !snapshot.Exists || len(snapshot.Archive) == 0 {
		t.Fatalf("snapshot %v %+v", err, snapshot)
	}
}

func TestNestedSkillImportAndDeleteDoNotAffectStandalone(t *testing.T) {
	r, root := nestedSkillFixture(t)
	archive := buildSkillImportZIP(t, []skillImportZIPEntry{{name: "SKILL.md", content: []byte("---\nname: demo\ndescription: Updated child\n---\nUpdated child\n")}})
	mutation, item, err := r.BeginImportEditableSkillArchive("suite/demo", bytes.NewReader(archive), int64(len(archive)), true)
	if err != nil {
		t.Fatal(err)
	}
	if item.ID != "suite/demo" {
		t.Fatal(item.ID)
	}
	if err := mutation.Commit(); err != nil {
		t.Fatal(err)
	}
	outside, _ := os.ReadFile(filepath.Join(root, "demo", "SKILL.md"))
	if !strings.Contains(string(outside), "Standalone") {
		t.Fatal("outside overwritten")
	}
	// A standalone update is scoped to the standalone directory even when the
	// same name is already installed in a package.
	mutation, _, err = r.BeginImportEditableSkillArchive("demo", bytes.NewReader(archive), int64(len(archive)), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := mutation.Rollback(); err != nil {
		t.Fatal(err)
	}
	outside, _ = os.ReadFile(filepath.Join(root, "demo", "SKILL.md"))
	if !strings.Contains(string(outside), "Standalone") {
		t.Fatal("standalone rollback failed")
	}
	if err := r.DeleteEditableSkill("suite/demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "suite", "demo")); !os.IsNotExist(err) {
		t.Fatal("member not removed")
	}
	if _, err := os.Stat(filepath.Join(root, "suite", "package.json")); err != nil {
		t.Fatal("package removed", err)
	}
	if _, err := os.Stat(filepath.Join(root, "demo", "SKILL.md")); err != nil {
		t.Fatal("standalone removed", err)
	}
}

func TestNestedSkillBoundaries(t *testing.T) {
	r, root := nestedSkillFixture(t)
	for _, key := range []string{"suite/../demo", "suite/demo/extra", "suite//demo", "/suite/demo", "suite\\demo", "suite/.hidden", "suite/CON", "suite/other.", "demo/sub-skills"} {
		if _, _, err := r.AdminSkill(key); err == nil {
			t.Errorf("accepted unsafe/non-package key %q", key)
		}
	}
	if _, _, err := r.AdminSkill("suite"); err == nil {
		t.Fatal("package root exposed as editable skill")
	}
	archive := buildSkillImportZIP(t, []skillImportZIPEntry{{name: "SKILL.md", content: []byte("---\nname: suite\ndescription: bad replacement\n---\nbody")}})
	if _, _, err := r.BeginImportEditableSkillArchive("suite", bytes.NewReader(archive), int64(len(archive)), true); err == nil {
		t.Fatal("standalone import replaced package")
	}
	if err := os.Symlink(filepath.Join(root, "suite"), filepath.Join(root, "linked")); err == nil {
		if _, err := r.ReadEditableSkillFile("linked/demo", "SKILL.md"); !errors.Is(err, ErrSkillSymlink) {
			t.Fatalf("package symlink accepted: %v", err)
		}
	}
}

func TestNestedSkillsRuntimeKeepsQualifiedIdentity(t *testing.T) {
	_, center := nestedSkillFixture(t)
	root := filepath.Dir(center)
	agents := filepath.Join(root, "agents")
	writeRuntimeAssemblerAgent(t, agents, "writer", []string{"demo", "suite/demo"})
	// The package key must not silently resolve to an Agent-private short-name
	// or nested lookalike directory.
	writeRuntimeAssemblerSkill(t, filepath.Join(agents, "writer", "skills", "suite", "demo"), "Wrong private")
	assembler, err := newRuntimeAgentAssembler(filepath.Join(root, "ru-agents"), center)
	if err != nil {
		t.Fatal(err)
	}
	loaded, admin, err := loadAgentsWithAdminAssembler(agents, filepath.Join(root, "chats"), true, assembler)
	if err != nil {
		t.Fatal(err)
	}
	def, ok := loaded["writer"]
	if !ok {
		t.Fatalf("invalid assembled agent: %+v", admin)
	}
	for key, want := range map[string]string{"demo": "Standalone", "suite/demo": "Packaged"} {
		item, ok, err := ResolveRuntimeSkillDefinition(def.RuntimeDir, key)
		if err != nil || !ok || item.Description != want {
			t.Fatalf("resolve %s: %+v %v", key, item, err)
		}
	}
}

func TestRegistryStartupMigratesLegacyPackagesBeforeLoadingSkills(t *testing.T) {
	root := t.TempDir()
	center := filepath.Join(root, "skills-center")
	agents := filepath.Join(root, "agents")
	writeRuntimeAssemblerFile(t, filepath.Join(center, "demo", "SKILL.md"), "---\nname: demo\ndescription: Legacy\n---\nLegacy body\n")
	writeRuntimeAssemblerFile(t, filepath.Join(center, ".package", "suite.json"), `{"schemaVersion":1,"id":"suite","name":"Suite","version":"1","sha256":"old","installedAt":1,"skills":[{"id":"demo","version":"1"}]}`)
	writeRuntimeAssemblerAgent(t, agents, "writer", []string{"demo", "suite/demo"})
	cfg := config.Config{Paths: config.PathsConfig{AgentsDir: agents, SkillsCenterDir: center, RUAgentsDir: filepath.Join(root, "ru-agents"), ChatsDir: filepath.Join(root, "chats")}}
	r, err := NewFileRegistry(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"demo", "suite/demo"} {
		if _, ok := r.SkillDefinition(key); !ok {
			t.Fatalf("missing %s after migration", key)
		}
	}
	if _, ok := r.AgentDefinition("writer"); !ok {
		t.Fatal("agent references invalid after startup migration")
	}
	if _, err := os.Stat(filepath.Join(center, ".package", "suite.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy record not archived: %v", err)
	}
	backups, err := filepath.Glob(filepath.Join(root, ".skill-package-backup-*"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups=%v err=%v", backups, err)
	}
	if _, err := NewFileRegistry(cfg, nil); err != nil {
		t.Fatalf("second startup: %v", err)
	}
	again, _ := filepath.Glob(filepath.Join(root, ".skill-package-backup-*"))
	if len(again) != 1 {
		t.Fatalf("migration repeated: %v", again)
	}
}
