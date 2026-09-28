package connector

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
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
