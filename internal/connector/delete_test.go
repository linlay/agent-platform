package connector

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDeletePackageRollbackAndStatePreservation(t *testing.T) {
	sources := Sources{ExternalRoot: t.TempDir(), StateRoot: t.TempDir()}
	target := filepath.Join(sources.ExternalRoot, "demo")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "payload"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(sources.StateRoot, "token.json")
	if err := os.WriteFile(state, []byte("retained"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	err := DeletePackage(context.Background(), sources, "demo", nil, func() error {
		calls++
		if calls == 1 {
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatal("package visible during deletion reload")
			}
			return errors.New("reload failed")
		}
		if data, err := os.ReadFile(filepath.Join(target, "payload")); err != nil || string(data) != "original" {
			t.Fatal("rollback lost package")
		}
		return nil
	})
	if !errors.Is(err, ErrDeleteReload) || calls != 2 {
		t.Fatalf("error %v, reloads %d", err, calls)
	}
	if err := DeletePackage(context.Background(), sources, "demo", nil, nil); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(sources.ExternalRoot); err != nil || len(entries) != 0 {
		t.Fatalf("leftover package or staging: %v %v", entries, err)
	}
	if data, err := os.ReadFile(state); err != nil || string(data) != "retained" {
		t.Fatal("deletion changed persistent state")
	}
	if err := DeletePackage(context.Background(), sources, "demo", nil, nil); !errors.Is(err, ErrPackageNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestDeletePackageRejectsUnsafeTargetsAndCanceledRequests(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skip(err)
	}
	if err := os.Mkdir(filepath.Join(root, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	reload := func() error { t.Fatal("rejected deletion reloaded catalog"); return nil }
	for _, id := range []string{"", "../escape", "builtin.dbx", "BUILTIN.dbx", "linked"} {
		if err := DeletePackage(context.Background(), Sources{ExternalRoot: root}, id, nil, reload); err == nil {
			t.Fatalf("accepted %q", id)
		}
	}
	blocked := errors.New("mounted")
	if err := DeletePackage(context.Background(), Sources{ExternalRoot: root}, "demo", func(string) error { return blocked }, reload); !errors.Is(err, blocked) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := DeletePackage(ctx, Sources{ExternalRoot: root}, "demo", nil, reload); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "demo")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Fatal("symlink deletion touched external data")
	}
}
