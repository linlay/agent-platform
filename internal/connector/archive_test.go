package connector

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func archiveFixture(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var data bytes.Buffer
	z := zip.NewWriter(&data)
	for name, content := range files {
		f, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestImportArchivePublishesWholePackageAndRollsBack(t *testing.T) {
	root := t.TempDir()
	sources := Sources{ExternalRoot: root}
	files := map[string]string{"connector.json": `{"id":"demo","name":"Demo","version":"1.0.0","type":"cli","auth_mode":"none"}`, "cli.json": `{}`, "old.txt": "previous"}
	install := func(overwrite bool, reload func() error) (Package, error) {
		data := archiveFixture(t, files)
		return ImportArchive(context.Background(), sources, bytes.NewReader(data), int64(len(data)), overwrite, nil, reload)
	}
	if _, err := install(false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := install(false, nil); !errors.Is(err, ErrPackageExists) {
		t.Fatalf("conflict: %v", err)
	}
	delete(files, "old.txt")
	files["new.txt"] = "next"
	calls := 0
	_, err := install(true, func() error {
		calls++
		if calls == 1 {
			return errors.New("reload failure")
		}
		return nil
	})
	if err == nil || calls != 2 {
		t.Fatalf("reload/rollback: %v calls %d", err, calls)
	}
	if data, err := os.ReadFile(filepath.Join(root, "demo", "old.txt")); err != nil || string(data) != "previous" {
		t.Fatal("previous package not restored")
	}
	if _, err := os.Stat(filepath.Join(root, "demo", "new.txt")); !os.IsNotExist(err) {
		t.Fatal("failed package leaked")
	}
	if _, err := install(true, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "demo", "old.txt")); !os.IsNotExist(err) {
		t.Fatal("overwrite retained stale file")
	}
	// Explicit overwrite can repair a malformed previous package.
	if err := os.WriteFile(filepath.Join(root, "demo", "cli.json"), []byte("broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := install(true, nil); err != nil {
		t.Fatalf("cannot repair malformed package: %v", err)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 2 || entries[0].Name() != ".cli-demo.lock" || entries[1].Name() != "demo" {
		t.Fatalf("staging leaked: %v", entries)
	}
}

func TestImportArchiveRejectsUnsafeAndReservedPackages(t *testing.T) {
	for _, extra := range []string{"../escape", "/absolute", "bin/../escape", "a\\b", "CON.txt", "a/x", "connector.json/child"} {
		t.Run(extra, func(t *testing.T) {
			files := map[string]string{"connector.json": `{"id":"demo","name":"Demo","version":"1.0.0","type":"cli","auth_mode":"none"}`, "cli.json": "{}", extra: "bad"}
			if extra == "a/x" {
				files["A/y"] = "case collision"
			}
			data := archiveFixture(t, files)
			if _, err := ImportArchive(context.Background(), Sources{ExternalRoot: t.TempDir()}, bytes.NewReader(data), int64(len(data)), false, nil, nil); err == nil {
				t.Fatal("accepted unsafe archive")
			}
		})
	}
	data := archiveFixture(t, map[string]string{"connector.json": `{"id":"builtin.demo","name":"Demo","version":"1.0.0","type":"cli","auth_mode":"none"}`, "cli.json": "{}"})
	if _, err := ImportArchive(context.Background(), Sources{ExternalRoot: t.TempDir()}, bytes.NewReader(data), int64(len(data)), true, nil, nil); !errors.Is(err, ErrBuiltinReadOnly) {
		t.Fatal(err)
	}
}

func TestImportArchiveAcceptsSingleWrapperAndValidatesBeforePublish(t *testing.T) {
	root := t.TempDir()
	data := archiveFixture(t, map[string]string{"demo/connector.json": `{"id":"demo","name":"Demo","version":"1.0.0","type":"cli","auth_mode":"none"}`, "demo/cli.json": "{}"})
	_, err := ImportArchive(context.Background(), Sources{ExternalRoot: root}, bytes.NewReader(data), int64(len(data)), false, func(packages []Package) error {
		if len(packages) != 1 || packages[0].ID != "demo" {
			t.Fatal(packages)
		}
		if _, err := os.Stat(filepath.Join(root, "demo")); !os.IsNotExist(err) {
			t.Fatal("published before validation")
		}
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
}
