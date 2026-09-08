package connector

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestConnectorStateMigrationRetainsCredentialsCLIAndOtherNamespaces(t *testing.T) {
	for _, custom := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "custom"}[custom], func(t *testing.T) {
			s := runtimeFixture(t)
			old := filepath.Join(filepath.Dir(s.ExternalRoot), "connector-state")
			if custom {
				old = filepath.Join(t.TempDir(), "old-state")
				s.LegacyStateRoot = old
				s.StateRoot = filepath.Join(t.TempDir(), "shared-state", "connectors")
			}
			files := map[string]string{
				".state/tmeet/config/token.json":             "cli-token",
				".state/tmeet/npm/node_modules/tmeet/cli.js": "executable",
				".state/tmeet/npm-cache/download":            "cached",
				".state/docs/oauth.json":                     "oauth-token",
				".state/docs/oauth.lock":                     "",
				".credentials/docs.json":                     "static-token",
			}
			for path, content := range files {
				putRuntimeFile(t, filepath.Join(old, path), content)
				if err := os.Chmod(filepath.Join(old, path), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			bin := filepath.Join(old, ".state/tmeet/npm/node_modules/.bin")
			if err := os.MkdirAll(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("../tmeet/cli.js", filepath.Join(bin, "tmeet")); err != nil {
				t.Skip(err)
			}
			empty := filepath.Join(old, ".state/tmeet/config/empty")
			if err := os.MkdirAll(empty, 0o700); err != nil {
				t.Fatal(err)
			}
			other := filepath.Join(filepath.Dir(s.StateRoot), "other", "session")
			putRuntimeFile(t, other, "other-module")
			for i := 0; i < 2; i++ {
				if err := s.MigrateLegacy(""); err != nil {
					t.Fatal(err)
				}
			}
			for path, want := range map[string]string{
				"tmeet/config/token.json":           "cli-token",
				"tmeet/npm/node_modules/.bin/tmeet": "executable",
				"tmeet/npm-cache/download":          "cached",
				"docs/oauth.json":                   "oauth-token",
				"docs/oauth.lock":                   "",
				"docs/credentials.json":             "static-token",
			} {
				file := filepath.Join(s.StateRoot, path)
				data, err := os.ReadFile(file)
				if err != nil || string(data) != want {
					t.Fatalf("lost %s: %v", path, err)
				}
				if runtime.GOOS != "windows" {
					info, err := os.Stat(file)
					if err != nil || info.Mode().Perm() != 0o600 {
						t.Fatalf("permissions changed: %s %v", path, err)
					}
				}
			}
			if _, err := os.Stat(filepath.Join(s.StateRoot, "tmeet/config/empty")); err != nil {
				t.Fatal(err)
			}
			if data, err := os.ReadFile(other); err != nil || string(data) != "other-module" {
				t.Fatal("other module changed", err)
			}
			if _, err := os.Lstat(old); !os.IsNotExist(err) {
				t.Fatal("old state root retained", err)
			}
			if _, err := os.Lstat(filepath.Join(s.StateRoot, ".state")); !os.IsNotExist(err) {
				t.Fatal("extra .state layer created")
			}
		})
	}
}

func TestConnectorStateMigrationRejectsCredentialCollisionBeforeMutation(t *testing.T) {
	s := runtimeFixture(t)
	old := filepath.Join(filepath.Dir(s.ExternalRoot), "connector-state")
	putRuntimeFile(t, filepath.Join(old, ".credentials/demo.json"), "old")
	putRuntimeFile(t, filepath.Join(old, ".state/demo/credentials.json"), "conflicting")
	putRuntimeFile(t, filepath.Join(old, ".state/aaa/oauth.json"), "must-stay")
	if err := s.MigrateLegacy(""); err == nil {
		t.Fatal("collision accepted")
	}
	if data, err := os.ReadFile(filepath.Join(old, ".state/aaa/oauth.json")); err != nil || string(data) != "must-stay" {
		t.Fatal("mutated before conflict check")
	}
	if _, err := os.Lstat(s.StateRoot); !os.IsNotExist(err) {
		t.Fatal("destination created before conflict check")
	}
}

func TestConnectorStateMigrationRestoresFilesAfterMoveFailure(t *testing.T) {
	s := runtimeFixture(t)
	old := filepath.Join(filepath.Dir(s.ExternalRoot), "connector-state")
	for _, file := range []string{"a", "b"} {
		putRuntimeFile(t, filepath.Join(old, ".state/demo", file), file)
	}
	count := 0
	err := s.migrateLegacy("", func(from, to string) error {
		count++
		if count == 2 {
			return errors.New("injected move failure")
		}
		return os.Rename(from, to)
	})
	if err == nil {
		t.Fatal("move failure ignored")
	}
	for _, file := range []string{"a", "b"} {
		if data, err := os.ReadFile(filepath.Join(old, ".state/demo", file)); err != nil || string(data) != file {
			t.Fatal("rollback lost source", err)
		}
	}
	if _, err := os.Lstat(s.StateRoot); !os.IsNotExist(err) {
		t.Fatal("rollback retained new state tree")
	}
	if err := s.MigrateLegacy(""); err != nil {
		t.Fatal("retry failed", err)
	}
}

func TestConnectorStateMigrationRejectsExistingDestination(t *testing.T) {
	s := runtimeFixture(t)
	old := filepath.Join(filepath.Dir(s.ExternalRoot), "connector-state")
	putRuntimeFile(t, filepath.Join(old, ".state/demo/oauth.json"), "old")
	putRuntimeFile(t, filepath.Join(s.StateRoot, "demo/oauth.json"), "current")
	if err := s.MigrateLegacy(""); err == nil {
		t.Fatal("existing destination overwritten")
	}
	if data, _ := os.ReadFile(filepath.Join(s.StateRoot, "demo/oauth.json")); string(data) != "current" {
		t.Fatal("destination changed")
	}
}
