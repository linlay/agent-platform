package connector

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/catalogorder"
)

func TestSourcesReserveBuiltinsAndIgnoreLegacyRuntimeCopies(t *testing.T) {
	builtin, external := t.TempDir(), t.TempDir()
	if err := WriteBuiltin(filepath.Join(builtin, "builtin.dbx"), "dbx", "1.2.3", "darwin"); err != nil {
		t.Fatal(err)
	}
	// A malformed legacy copy cannot override or prevent loading the bundle.
	if err := os.WriteFile(filepath.Join(external, "builtin.dbx"), []byte("invalid legacy copy"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(external, "remote"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"connector.json": `{"id":"remote","name":"Remote","version":"1.0.0","type":"cli","auth_mode":"none"}`,
		"cli.json":       `{}`,
	} {
		if err := os.WriteFile(filepath.Join(external, "remote", name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sources := Sources{BuiltinRoot: builtin, ExternalRoot: external}
	items, err := sources.Summaries()
	if err != nil || len(items) != 2 {
		t.Fatalf("union: %#v %v", items, err)
	}
	if !items[0].Builtin || !items[0].ReadOnly || items[0].CanDelete || items[1].Builtin || items[1].ReadOnly || !items[1].CanDelete {
		t.Fatalf("ownership: %#v", items)
	}
	pkg, err := sources.Load("builtin.dbx")
	if err != nil || pkg.Dir != filepath.Join(builtin, "builtin.dbx") {
		t.Fatalf("source: %q %v", pkg.Dir, err)
	}
	if _, err := (Sources{ExternalRoot: external}).Load("builtin.dbx"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy fallback: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(builtin, "builtin.dbx")); err != nil {
		t.Fatal(err)
	}
	if _, err := sources.Load("builtin.dbx"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing bundle fallback: %v", err)
	}
}

func TestBuiltinDefinitionRejectsMutationBeforeFilesystemOrReload(t *testing.T) {
	for _, id := range []string{"builtin.dbx", "builtin.httpx", "builtin.future", "BUILTIN.DBX"} {
		_, err := SaveDefinition(t.TempDir(), File{ID: id, File: "connector.json", Content: "{}"}, "", nil, func() error { t.Fatal("builtin mutation triggered reload"); return nil })
		if !errors.Is(err, ErrBuiltinReadOnly) {
			t.Fatalf("%s mutation: %v", id, err)
		}
	}
}

func TestSourcesIgnoreUserOrderMetadata(t *testing.T) {
	builtin, external := t.TempDir(), t.TempDir()
	if err := WriteBuiltin(filepath.Join(builtin, "builtin.dbx"), "dbx", "1.2.3", "darwin"); err != nil {
		t.Fatal(err)
	}
	sources := Sources{BuiltinRoot: builtin, ExternalRoot: external}
	before, err := sources.Summaries()
	if err != nil {
		t.Fatal(err)
	}
	store := catalogorder.NewFileOrderStore(external)
	if _, err := store.SetPinned("user:alice", "builtin.dbx", true); err != nil {
		t.Fatal(err)
	}
	// Atomic-write temporary files must also stay out of the package catalog.
	if err := os.WriteFile(filepath.Join(external, ".catalog-order-pending.json"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := sources.Summaries()
	if err != nil || len(after) != len(before) || after[0].ID != "builtin.dbx" {
		t.Fatalf("catalog after pin: %#v %v", after, err)
	}
	state, err := store.Read("user:alice")
	if err != nil || len(state.Order) != 1 || state.Order[0] != "builtin.dbx" {
		t.Fatalf("preference changed by catalog scan: %#v %v", state, err)
	}
}

func TestSourcesStillValidatePackageRootsBesideOrderMetadata(t *testing.T) {
	for _, kind := range []string{"order-directory", "order-symlink", "other-file"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			order := filepath.Join(root, catalogorder.OrderFileName)
			switch kind {
			case "order-directory":
				if err := os.Mkdir(order, 0o755); err != nil {
					t.Fatal(err)
				}
			case "order-symlink":
				if err := os.Symlink(t.TempDir(), order); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			case "other-file":
				if err := os.WriteFile(filepath.Join(root, "broken-package"), []byte("broken"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := (Sources{ExternalRoot: root}).LoadAll(); err == nil {
				t.Fatal("invalid package root silently ignored")
			}
		})
	}
}
