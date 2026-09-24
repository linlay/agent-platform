package connectormigrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDesktopMigrationPreviewApplyRollback(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agents", "demo", "agent.yml")
	os.MkdirAll(filepath.Dir(path), 0700)
	original := []byte("key: demo\nmode: REACT\n# preserve comment\nruntimeConfig:\n  env:\n    SECRET: ${DONT_EXPAND}\ntoolConfig:\n  tools:\n    - desktop_action\n    - file_read\nskillConfig:\n  skills:\n    - desktop-action\n    - office\n")
	os.WriteFile(path, original, 0600)
	skill := filepath.Join(root, "skills-center", "desktop-action")
	os.MkdirAll(skill, 0700)
	os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("original"), 0600)
	plan, err := PreviewDesktop(root)
	if err != nil || len(plan.Changes) != 1 {
		t.Fatalf("preview: %#v %v", plan, err)
	}
	if len(plan.Changes[0].AddedTools) != 1 || plan.Changes[0].AddedTools[0] != "desktop_cdp" {
		t.Fatal("missing expansion report")
	}
	if _, err := ApplyDesktop(plan, false, false); err == nil {
		t.Fatal("unacknowledged expansion applied")
	}
	applied, err := ApplyDesktop(plan, true, false)
	if err != nil {
		t.Fatal(err)
	}
	changed, _ := os.ReadFile(path)
	if !strings.Contains(string(changed), "${DONT_EXPAND}") || !strings.Contains(string(changed), "# preserve comment") {
		t.Fatal("unrelated YAML changed")
	}
	if _, err := os.Stat(skill); !os.IsNotExist(err) {
		t.Fatal("duplicate active skill remains")
	}
	next, err := PreviewDesktop(root)
	if err != nil || len(next.Changes) != 0 || len(next.RetireSkills) != 0 {
		t.Fatal("migration not idempotent")
	}
	if err := RollbackDesktop(applied.Backup); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(path)
	if string(restored) != string(original) {
		t.Fatal("rollback changed bytes")
	}
	if _, err := os.Stat(skill); err != nil {
		t.Fatal("skill not restored")
	}
}
func TestDesktopMigrationRejectsConcurrentChanges(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "agents"), 0700)
	path := filepath.Join(root, "agents", "a.yml")
	os.WriteFile(path, []byte("key: a\ntoolConfig:\n  tools:\n    - desktop_action\n    - desktop_cdp\n"), 0600)
	plan, err := PreviewDesktop(root)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte("key: changed\n"), 0600)
	if _, err := ApplyDesktop(plan, true, false); err == nil {
		t.Fatal("concurrent edit overwritten")
	}
}
