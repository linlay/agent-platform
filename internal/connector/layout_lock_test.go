package connector

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSharedLayoutUsesDedicatedRuntimeLockDirectory(t *testing.T) {
	root := t.TempDir()
	s := Sources{ExternalRoot: filepath.Join(root, "connectors-center")}
	if err := s.ensureSharedLayout(); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(root, ".lock", "shared-connector-layout.lock")
	if _, err := os.Stat(lock); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".cli-shared-connector-layout.lock")); !os.IsNotExist(err) {
		t.Fatalf("legacy lock created: %v", err)
	}
	release, err := acquireSharedLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if other, err := acquireOperationFile(lock); !errors.Is(err, ErrBusy) {
		if other != nil {
			other()
		}
		t.Fatalf("lock not exclusive: %v", err)
	}
	release()
	if _, err := os.Stat(lock); err != nil {
		t.Fatalf("lock removed after release: %v", err)
	}
	again, err := acquireSharedLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	again()
	if err := s.ensureSharedLayout(); err != nil {
		t.Fatal(err)
	}
}

func TestSharedLayoutRejectsInvalidLockDirectory(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "symlink"}[symlink], func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, ".lock")
			if symlink {
				if runtime.GOOS == "windows" {
					t.Skip("symlink privileges vary")
				}
				if err := os.Symlink(t.TempDir(), path); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
				t.Fatal(err)
			}
			if release, err := acquireSharedLayout(root); err == nil {
				release()
				t.Fatal("invalid lock directory accepted")
			}
		})
	}
}

func TestConnectorLockScopesAndPaths(t *testing.T) {
	s := runtimeFixture(t)
	root := filepath.Dir(s.ExternalRoot)
	operation, err := AcquireOperation(s.ExternalRoot, "builtin.dbx")
	if err != nil {
		t.Fatal(err)
	}
	defer operation()
	if other, err := AcquireOperation(s.ExternalRoot, "builtin.dbx"); !errors.Is(err, ErrBusy) {
		if other != nil {
			other()
		}
		t.Fatalf("operation did not exclude peer: %v", err)
	}
	// Installing a runtime version uses its own scope, not the source operation lock.
	pkg, err := s.Load("builtin.dbx")
	if err != nil {
		t.Fatal(err)
	}
	installed, err := s.InstallShared(pkg)
	if err != nil {
		t.Fatal(err)
	}
	hold, err := RetainShared(installed.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	install, err := acquireSharedOperation(s.SharedRoot(), pkg.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer install()
	paths := []string{
		"shared-connector-layout.lock", "connectors/assembly.lock",
		"connectors/install/builtin.dbx.lock", "connectors/operations/builtin.dbx.lock",
		"connectors/leases/builtin.dbx/" + filepath.Base(installed.Dir) + ".lock",
	}
	for _, rel := range paths {
		p := filepath.Join(root, ".lock", filepath.FromSlash(rel))
		if _, err := os.Stat(p); err != nil {
			t.Fatal(err)
		}
		if rel == "connectors/install/builtin.dbx.lock" || strings.HasPrefix(rel, "connectors/leases/") {
			if other, err := acquireOperationFile(p); !errors.Is(err, ErrBusy) {
				if other != nil {
					other()
				}
				t.Fatalf("lock not held at %s: %v", rel, err)
			}
		}
	}
	for _, dir := range []string{s.SharedRoot(), filepath.Dir(installed.Dir), s.ExternalRoot} {
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".lock") || strings.HasPrefix(e.Name(), ".lease-") {
				t.Fatalf("lock in content directory: %s", filepath.Join(dir, e.Name()))
			}
		}
	}
}

func TestSharedInitializationDoesNotMigrateUnmarkedDirectory(t *testing.T) {
	s := runtimeFixture(t)
	existing := filepath.Join(s.SharedRoot(), "manual.txt")
	putRuntimeFile(t, existing, "keep")
	if err := s.ensureSharedLayout(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(existing); err != nil || string(data) != "keep" {
		t.Fatalf("existing content changed: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(s.SharedRoot(), ".shared-v1")); err != nil || string(data) != "1\n" {
		t.Fatalf("missing marker: %v", err)
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(s.ExternalRoot), ".connector-layout-backup-*"))
	if err != nil || len(backups) != 0 {
		t.Fatalf("unexpected migration: %v %v", backups, err)
	}
}

func TestConnectorLocksRejectNestedSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privileges vary")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".lock"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, ".lock", "connectors")); err != nil {
		t.Fatal(err)
	}
	if release, err := AcquireOperation(filepath.Join(root, "connectors-center"), "demo"); err == nil {
		release()
		t.Fatal("accepted symlink lock directory")
	}
}
