package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/builtins"
)

func TestOfferUpdatePromotesVerifiedNewerCleanComponent(t *testing.T) {
	root, lockPath, collection, localCommit := promotionFixture(t, false)
	var output bytes.Buffer
	if err := offerUpdate(lockPath, collection, strings.NewReader("yes\n"), &output, true); err != nil {
		t.Fatalf("offerUpdate: %v", err)
	}
	updated, err := builtins.LoadLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	component := updated.Components[0]
	if component.Version != "v1.2.0" || component.Commit != localCommit {
		t.Fatalf("promoted component = version %q commit %q", component.Version, component.Commit)
	}
	if got := component.Targets["darwin-arm64"].Path; got != "dist/v1.2.0/dbx_v1.2.0_darwin_arm64.tar.gz" {
		t.Fatalf("promoted path = %q", got)
	}
	if !strings.Contains(output.String(), "v1.0.0 -> v1.2.0") || !strings.Contains(output.String(), "updated canonical builtins lock") {
		t.Fatalf("output = %q", output.String())
	}
	_ = root
}

func TestOfferUpdateDoesNotWriteWithoutInteractiveYes(t *testing.T) {
	for _, test := range []struct {
		name        string
		interactive bool
		answer      string
	}{
		{name: "declined", interactive: true, answer: "no\n"},
		{name: "non-interactive", interactive: false, answer: "yes\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, lockPath, collection, _ := promotionFixture(t, false)
			before, err := os.ReadFile(lockPath)
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := offerUpdate(lockPath, collection, strings.NewReader(test.answer), &output, test.interactive); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(lockPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("canonical lock changed without interactive yes")
			}
		})
	}
}

func TestOfferUpdateRejectsDirtyNewerComponent(t *testing.T) {
	_, lockPath, collection, _ := promotionFixture(t, true)
	before, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := offerUpdate(lockPath, collection, strings.NewReader("yes\n"), &output, true); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("dirty component changed canonical lock")
	}
	if !strings.Contains(output.String(), "uncommitted changes") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestOfferUpdatePromotesZeroPaddedPopplerVersionAndSource(t *testing.T) {
	root := t.TempDir()
	collection := filepath.Join(root, "collection")
	repository := filepath.Join(collection, "poppler-pdftotext")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "VERSION"), []byte("v26.08.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(repository, "dist", "v26.08.0", "poppler-pdftotext_v26.08.0_darwin_arm64.tar.gz")
	writePromotionArchive(t, archive, "runtime/bin/pdftotext")
	lock := builtins.Lock{SchemaVersion: 1, DefaultRoot: "../agent-platform-builtins", Components: []builtins.Component{{
		Name: "poppler-pdftotext", Version: "v26.07.0", Repository: "poppler-pdftotext", Source: "https://poppler.freedesktop.org/poppler-26.07.0.tar.xz", Kind: "archive-tree", Required: false,
		Targets: map[string]builtins.Target{"darwin-arm64": {
			Path: "dist/v26.07.0/poppler-pdftotext_v26.07.0_darwin_arm64.tar.gz", Format: "tar.gz", SHA256: strings.Repeat("b", 64),
			Tree: &builtins.TreeLayout{Root: "runtime", Outputs: []builtins.TreeOutput{{Path: "bin/pdftotext", Type: "file"}}},
		}},
	}}}
	lockPath := filepath.Join(root, "builtins.lock.json")
	payload, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, append(payload, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := offerUpdate(lockPath, collection, strings.NewReader("yes\n"), &output, true); err != nil {
		t.Fatal(err)
	}
	updated, err := builtins.LoadLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	component := updated.Components[0]
	if component.Version != "v26.08.0" || component.Source != "https://poppler.freedesktop.org/poppler-26.08.0.tar.xz" {
		t.Fatalf("promoted Poppler = version %q source %q", component.Version, component.Source)
	}
}

func TestCompareSemanticVersions(t *testing.T) {
	for _, test := range []struct {
		left  string
		right string
		want  int
	}{
		{left: "v1.2.0", right: "v1.1.9", want: 1},
		{left: "2.0.0", right: "v2.0.0", want: 0},
		{left: "v1.0.0-rc.2", right: "v1.0.0-rc.10", want: -1},
		{left: "v1.0.0", right: "v1.0.0-rc.1", want: 1},
		{left: "v26.08.0", right: "v26.07.0", want: 1},
	} {
		got, err := compareSemanticVersions(test.left, test.right)
		if err != nil {
			t.Fatalf("compare %s and %s: %v", test.left, test.right, err)
		}
		if got != test.want {
			t.Fatalf("compare %s and %s = %d, want %d", test.left, test.right, got, test.want)
		}
	}
}

func promotionFixture(t *testing.T, dirty bool) (string, string, string, string) {
	t.Helper()
	root := t.TempDir()
	collection := filepath.Join(root, "collection")
	repository := filepath.Join(collection, "dbx")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"VERSION":    "v1.2.0\n",
		".gitignore": "dist/\n",
		"main.go":    "package main\n",
	} {
		if err := os.WriteFile(filepath.Join(repository, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	localCommit := initGitRepository(t, repository)
	if dirty {
		if err := os.WriteFile(filepath.Join(repository, "main.go"), []byte("package main\n// dirty\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	archive := filepath.Join(repository, "dist", "v1.2.0", "dbx_v1.2.0_darwin_arm64.tar.gz")
	writePromotionArchive(t, archive, "dbx")

	lock := builtins.Lock{SchemaVersion: 1, DefaultRoot: "../agent-platform-builtins", Components: []builtins.Component{{
		Name: "dbx", Version: "v1.0.0", Repository: "dbx", Source: "https://example.invalid/dbx.git", Commit: strings.Repeat("a", 40), Kind: "archive", Required: true,
		Targets: map[string]builtins.Target{"darwin-arm64": {
			Path: "dist/v1.0.0/dbx_v1.0.0_darwin_arm64.tar.gz", Format: "tar.gz", Entry: "dbx", Output: "dbx", SHA256: strings.Repeat("b", 64),
		}},
	}}}
	lockPath := filepath.Join(root, "builtins.lock.json")
	payload, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, append(payload, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, lockPath, collection, localCommit
}

func writePromotionArchive(t *testing.T, archivePath, entry string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	payload := []byte("binary")
	if err := tarWriter.WriteHeader(&tar.Header{Name: entry, Mode: 0o755, Size: int64(len(payload))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
