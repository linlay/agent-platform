package connector

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
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
