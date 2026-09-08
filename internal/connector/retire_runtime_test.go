package connector

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRetireSharedRuntimeKeepsBackupAndCentralState(t *testing.T) {
	s := runtimeFixture(t)
	root := filepath.Dir(s.ExternalRoot)
	old := filepath.Join(root, "ru-connectors", "old", "skills", "SKILL.md")
	putRuntimeFile(t, old, "old skill")
	state := filepath.Join(s.StateRoot, "old", "oauth.json")
	putRuntimeFile(t, state, "secret")
	for i := 0; i < 2; i++ {
		if err := s.RetireSharedRuntime(root); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "ru-connectors")); !os.IsNotExist(err) {
		t.Fatal("obsolete runtime remains")
	}
	files, err := filepath.Glob(filepath.Join(root, ".connector-layout-backup-*", "ru-connectors", "old", "skills", "SKILL.md"))
	if err != nil || len(files) != 1 {
		t.Fatal("missing or duplicated backup")
	}
	if data, err := os.ReadFile(files[0]); err != nil || string(data) != "old skill" {
		t.Fatal("backup content changed")
	}
	if data, err := os.ReadFile(state); err != nil || string(data) != "secret" {
		t.Fatal("credential state changed")
	}
}

func TestMaterializeRejectsPersistentStateInsidePackage(t *testing.T) {
	for _, name := range []string{".state", ".credentials", "connector-state", "credentials.json", "oauth.json"} {
		t.Run(name, func(t *testing.T) {
			s := runtimeFixture(t)
			putRuntimeFile(t, filepath.Join(s.BuiltinRoot, "builtin.dbx", name), "private state")
			if _, err := s.Materialize(filepath.Join(t.TempDir(), "connectors"), []string{"builtin.dbx"}); err == nil {
				t.Fatal("packaged state accepted")
			}
		})
	}
}
