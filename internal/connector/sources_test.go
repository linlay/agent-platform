package connector

import (
	"agent-platform/internal/connectortest"
	"bytes"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/catalogorder"
)

func TestSourcesReserveBuiltinsAndIgnoreLegacyRuntimeCopies(t *testing.T) {
	builtin, external := t.TempDir(), t.TempDir()
	if err := connectortest.WriteCLI(filepath.Join(builtin, "builtin.dbx"), "dbx", "1.2.3"); err != nil {
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
		"connector.json": `{"id":"remote","name":"Remote","version":"1.0.0","type":"cli","authMode":"self"}`,
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
	if err := connectortest.WriteCLI(filepath.Join(builtin, "builtin.dbx"), "dbx", "1.2.3"); err != nil {
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
	for _, name := range []string{"README.md", "notes.txt", "broken-package"} {
		if err := os.WriteFile(filepath.Join(external, name), []byte("not a package"), 0o600); err != nil {
			t.Fatal(err)
		}
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
	for _, kind := range []string{"order-directory", "order-symlink", "invalid-package-directory"} {
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
			case "invalid-package-directory":
				if err := os.Mkdir(filepath.Join(root, "broken-package"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			sources := Sources{ExternalRoot: root}
			var warnings bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&warnings)
			t.Cleanup(func() { log.SetOutput(previous) })
			if packages, err := sources.LoadAll(); err != nil || len(packages) != 0 {
				t.Fatalf("invalid package blocked enumeration: %v %v", packages, err)
			}
			if !strings.Contains(warnings.String(), "warning: skipping invalid connector") {
				t.Fatalf("missing warning: %s", warnings.String())
			}
			if packages, err := sources.loadAllExcept(""); err != nil || len(packages) != 0 {
				t.Fatalf("invalid installed package blocked import validation: %v %v", packages, err)
			}
		})
	}
}

func TestSourcesKeepHealthyPackagesBesideInvalidTokenSchema(t *testing.T) {
	root := t.TempDir()
	healthy := filepath.Join(root, "healthy")
	if err := os.MkdirAll(healthy, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"connector.json": `{"id":"healthy","name":"Healthy","version":"1.0.0","type":"cli","authMode":"none"}`,
		"cli.json":       `{}`,
	} {
		if err := os.WriteFile(filepath.Join(healthy, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(root, "tencent-map")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"connector.json":    `{"id":"tencent-map","name":"Map","version":"1.0.0","type":"mcp","authMode":"token"}`,
		"token-schema.json": `{"title_en":"Map","fields":[{"key":"TOKEN"}]}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	sources := Sources{ExternalRoot: root}
	packages, err := sources.LoadAll()
	if err != nil || len(packages) != 1 || packages[0].ID != "healthy" {
		t.Fatalf("healthy packages unavailable: %v %v", packages, err)
	}
	if _, err := sources.Load("tencent-map"); err == nil || !strings.Contains(err.Error(), `unknown field "title_en"`) {
		t.Fatalf("direct loading must still validate schema: %v", err)
	}
	items, err := sources.Summaries()
	if err != nil || len(items) != 1 || items[0].ID != "healthy" {
		t.Fatalf("healthy catalog unavailable: %v %v", items, err)
	}
	admin, err := sources.AdminSummaries()
	if err != nil || len(admin) != 2 || admin[1].ID != "tencent-map" || admin[1].Status != "invalid" || !strings.Contains(admin[1].Error, `unknown field "title_en"`) || admin[1].HasMCP || !admin[1].CanDelete {
		t.Fatalf("invalid package diagnostic missing or capabilities exposed: %+v %v", admin, err)
	}
}

func TestSourcesRootTraversalStillFails(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(root, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := (Sources{ExternalRoot: root}).LoadAll(); err == nil {
		t.Fatal("source-root traversal error ignored")
	}
	if _, err := (Sources{ExternalRoot: root}).loadAllExcept("demo"); err == nil {
		t.Fatal("import ignored source-root traversal error")
	}
}
