package connector

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStateControlReadDoesNotCreateAndLockSurvivesPrivateDeletion(t *testing.T) {
	root := filepath.Join(t.TempDir(), "custom-state", "connectors")
	if control, err := ReadStateControl(root, "demo"); err != nil || control != (StateControl{}) {
		t.Fatalf("missing control: %+v %v", control, err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("read created state root: %v", err)
	}
	release, err := AcquireStateLock(t.Context(), root, "demo")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	private, _ := StateDir(root, "demo")
	if _, err := os.Stat(private); !os.IsNotExist(err) {
		t.Fatalf("locking created private state: %v", err)
	}
	control := StateControl{Generation: "new-installation", Deleted: true}
	if err := WriteStateControl(root, "demo", control); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(private); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	if other, err := AcquireStateLock(ctx, root, "demo"); !errors.Is(err, context.DeadlineExceeded) {
		if other != nil {
			other()
		}
		t.Fatalf("private deletion split lock: %v", err)
	}
	release()
	again, err := AcquireStateLock(t.Context(), root, "demo")
	if err != nil {
		t.Fatal(err)
	}
	again()
	got, err := ReadStateControl(root, "demo")
	if err != nil || got != control {
		t.Fatalf("stable control: %+v %v", got, err)
	}
	for _, id := range []string{"demo", "demo.delete", "demo.lock"} {
		path, err := stateControlPath(root, id, ".json", true)
		if err != nil || path != filepath.Join(root, ".lock", id, "control.json") {
			t.Fatalf("control namespace collision: %s %v", path, err)
		}
	}
}

func TestStateControlRejectsUnsafePaths(t *testing.T) {
	for _, entry := range []string{"root", ".lock", ".lock/demo", ".lock/demo/control.json", ".lock/demo/state.lock"} {
		t.Run(entry, func(t *testing.T) {
			base, outside := t.TempDir(), t.TempDir()
			root := filepath.Join(base, "connectors")
			path := root
			if entry != "root" {
				path = filepath.Join(root, entry)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, path); err != nil {
				t.Skip(err)
			}
			if _, err := ReadStateControl(root, "demo"); err == nil && entry != ".lock/demo/state.lock" {
				t.Fatal("read followed unsafe control path")
			}
			if entry == ".lock/demo/control.json" {
				if err := WriteStateControl(root, "demo", StateControl{}); err == nil {
					t.Fatal("write followed unsafe control file")
				}
			} else if release, err := AcquireStateLock(t.Context(), root, "demo"); err == nil {
				release()
				t.Fatal("lock followed unsafe path")
			}
		})
	}
}
