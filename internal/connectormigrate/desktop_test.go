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
	original := []byte("key: demo\nmode: REACT\n# preserve comment\nruntimeConfig:\n  env:\n    SECRET: ${DONT_EXPAND}\ntoolConfig:\n  tools:\n    - desktop_action\n    - file_read\nskillConfig:\n  skills:\n    - desktop-action\n    - office\nconnectorConfig:\n  connectors:\n    - builtin.platform-control\n")
	os.WriteFile(path, original, 0600)
	skill := filepath.Join(root, "skills-center", "desktop-action")
	os.MkdirAll(skill, 0700)
	os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("original"), 0600)
	plan, err := PreviewDesktop(root)
	if err != nil || len(plan.Changes) != 1 {
		t.Fatalf("preview: %#v %v", plan, err)
	}
	if len(plan.Changes[0].AddedTools) != 0 {
		t.Fatal("unexpected authorization expansion")
	}
	applied, err := ApplyDesktop(plan, true)
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
	os.WriteFile(path, []byte("key: a\ntoolConfig:\n  tools:\n    - platform_control\n    - desktop_cdp\n"), 0600)
	plan, err := PreviewDesktop(root)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte("key: changed\n"), 0600)
	if _, err := ApplyDesktop(plan, true); err == nil {
		t.Fatal("concurrent edit overwritten")
	}
}

func TestDesktopMigrationMapsLegacyMountsToWebControl(t *testing.T) {
	for _, tc := range []struct {
		name, mounts string
		want         []string
		unwanted     string
	}{
		// The former web-only variant becomes builtin.web-control alone.
		{"web variant", "    - builtin.desktop-web\n", []string{`"builtin.web-control"`}, `"builtin.desktop"`},
	} {
		root := t.TempDir()
		path := filepath.Join(root, "agents", "demo", "agent.yml")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		// Keys without a value ("tools:") must not abort the whole plan.
		if err := os.WriteFile(path, []byte("key: demo\nmode: GENERAL\ntoolConfig:\n  tools:\nskillConfig:\n  skills:\nconnectorConfig:\n  connectors:\n"+tc.mounts), 0600); err != nil {
			t.Fatal(err)
		}
		plan, err := PreviewDesktop(root)
		if err != nil || len(plan.Changes) != 1 {
			t.Fatalf("%s preview: %+v %v", tc.name, plan, err)
		}
		change := plan.Changes[0]
		if strings.Contains(string(change.Data), tc.unwanted) {
			t.Fatalf("%s: %s", tc.name, change.Data)
		}
		for _, id := range tc.want {
			if !strings.Contains(string(change.Data), id) {
				t.Fatalf("%s misses %s: %s", tc.name, id, change.Data)
			}
		}
		if _, err := ApplyDesktop(plan, true); err != nil {
			t.Fatalf("%s apply: %v", tc.name, err)
		}
		if next, err := PreviewDesktop(root); err != nil || len(next.Changes) != 0 {
			t.Fatalf("%s is not idempotent: %+v %v", tc.name, next, err)
		}
	}
}

func TestDesktopMigrationRequiresExplicitManagementGrant(t *testing.T) {
	for _, config := range []string{"toolConfig:\n  tools:\n    - desktop_action\n", "connectorConfig:\n  connectors:\n    - builtin.desktop\n"} {
		root := t.TempDir()
		path := filepath.Join(root, "agents", "demo", "agent.yml")
		os.MkdirAll(filepath.Dir(path), 0700)
		original := []byte("key: demo\n" + config)
		os.WriteFile(path, original, 0600)
		plan, err := PreviewDesktop(root)
		if err != nil || len(plan.Pending) != 1 || len(plan.Changes) != 0 {
			t.Fatalf("plan: %+v %v", plan, err)
		}
		if _, err := ApplyDesktop(plan, true); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(path)
		if string(got) != string(original) {
			t.Fatal("unresolved Agent changed")
		}
	}
	root := t.TempDir()
	path := filepath.Join(root, "agents", "demo", "agent.yml")
	os.MkdirAll(filepath.Dir(path), 0700)
	os.WriteFile(path, []byte("key: demo\ntoolConfig:\n  tools:\n    - platform_control\n    - file_read\n"), 0600)
	plan, err := PreviewDesktop(root)
	if err != nil || len(plan.Changes) != 1 || strings.Contains(string(plan.Changes[0].Data), "builtin.platform-control") {
		t.Fatalf("implicit management grant: %+v %v", plan, err)
	}
}
