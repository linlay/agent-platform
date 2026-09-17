package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/builtins"
)

func TestZipPreservesConnectorTree(t *testing.T) {
	for _, separator := range []string{"/", "\\"} {
		t.Run(separator, func(t *testing.T) {
			root := t.TempDir()
			relative := "connectors/builtin.dbx"
			if err := os.MkdirAll(filepath.Join(root, relative, "bin/libs"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, relative, "bin/dbx.exe"), []byte("payload"), 0644); err != nil {
				t.Fatal(err)
			}
			tree := []builtins.TreeOutput{{Path: relative, Type: "dir"}}
			want, err := builtins.TreeDigest(root, tree)
			if err != nil {
				t.Fatal(err)
			}
			archivePath := filepath.Join(t.TempDir(), "bundle.zip")
			file, err := os.Create(archivePath)
			if err != nil {
				t.Fatal(err)
			}
			writer := zip.NewWriter(file)
			// Intentionally no directory attributes: reproduce Windows .NET ZIPs.
			for _, name := range []string{relative + "/bin/libs/", relative + "/bin/dbx.exe"} {
				entry, err := writer.Create(strings.ReplaceAll(name, "/", separator))
				if err != nil {
					t.Fatal(err)
				}
				if strings.HasSuffix(name, ".exe") {
					if _, err := entry.Write([]byte("payload")); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			extracted := t.TempDir()
			if err := extractZip(archivePath, extracted); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(filepath.Join(extracted, relative, "bin/libs"))
			if err != nil || !info.IsDir() {
				t.Fatalf("empty directory not preserved: %v", err)
			}
			got, err := builtins.TreeDigest(extracted, tree)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("tree changed: got %s, want %s", got, want)
			}
		})
	}
}

func TestTarGzPreservesConnectorTree(t *testing.T) {
	root := t.TempDir()
	relative := "connectors/builtin.dbx"
	if err := os.MkdirAll(filepath.Join(root, relative, "bin/libs"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, relative, "bin/dbx"), []byte("payload"), 0755); err != nil {
		t.Fatal(err)
	}
	tree := []builtins.TreeOutput{{Path: relative, Type: "dir"}}
	want, err := builtins.TreeDigest(root, tree)
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "bundle.tar.gz")
	createTarGz(t, root, archivePath)
	extracted := t.TempDir()
	if err := extractTarGz(archivePath, extracted); err != nil {
		t.Fatal(err)
	}
	got, err := builtins.TreeDigest(extracted, tree)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("tar tree changed: got %s, want %s", got, want)
	}
}
