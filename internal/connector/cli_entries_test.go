package connector

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCLIEntriesFreezeOnlyPackageEntrypoints(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(filepath.Join(bin, "libs"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"tool": "#!/bin/sh\necho ok", "launcher.cjs": "console.log('ok')", "tool.cmd": "@echo off", "notes.json": "{}", "libs/hidden": "library"} {
		mode := os.FileMode(0600)
		if name == "tool" || name == "libs/hidden" {
			mode = 0700
		}
		if err := os.WriteFile(filepath.Join(bin, name), []byte(data), mode); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := SnapshotCLIEntries("demo", root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("unexpected entries: %+v", entries)
	}
	for _, entry := range entries {
		if entry.ConnectorID != "demo" || len(entry.SHA256) != 64 {
			t.Fatalf("invalid snapshot: %+v", entry)
		}
	}
	oldHash := entries[0].SHA256
	if err := os.WriteFile(entries[0].Path, []byte("changed"), 0700); err != nil {
		t.Fatal(err)
	}
	fresh, err := SnapshotCLIEntries("demo", root)
	if err != nil {
		t.Fatal(err)
	}
	if fresh[0].SHA256 == oldHash || entries[0].SHA256 != oldHash {
		t.Fatal("published snapshot was not independent of new content")
	}
	outside := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(outside, []byte("outside"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(bin, "escape")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := SnapshotCLIEntries("demo", root); err == nil {
		t.Fatal("snapshot accepted an entry outside the package")
	}
}
