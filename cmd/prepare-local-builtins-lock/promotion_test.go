package main

import (
	"archive/tar"
	"archive/zip"
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
	_, lockPath, collection, durable, localCommit := promotionFixture(t, false)
	var output bytes.Buffer
	if err := offerUpdate(lockPath, collection, durable, "darwin/arm64", strings.NewReader("yes\n"), &output, true); err != nil {
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
	if got := component.Targets["windows-amd64"]; got.Version != "v1.0.0" || got.Path != "dist/v1.0.0/dbx_v1.0.0_windows_amd64.zip" || got.SHA256 != strings.Repeat("c", 64) {
		t.Fatalf("cross-built Windows target changed = %#v", got)
	}
	if _, err := os.Stat(filepath.Join(durable, "dbx", component.Targets["darwin-arm64"].Path)); err != nil {
		t.Fatalf("durable artifact: %v", err)
	}
	if !strings.Contains(output.String(), "v1.0.0 -> v1.2.0") || !strings.Contains(output.String(), "updated canonical target darwin-arm64") {
		t.Fatalf("output = %q", output.String())
	}
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
			_, lockPath, collection, durable, _ := promotionFixture(t, false)
			before, err := os.ReadFile(lockPath)
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := offerUpdate(lockPath, collection, durable, "darwin/arm64", strings.NewReader(test.answer), &output, test.interactive); err != nil {
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
	_, lockPath, collection, durable, _ := promotionFixture(t, true)
	before, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := offerUpdate(lockPath, collection, durable, "darwin/arm64", strings.NewReader("yes\n"), &output, true); err != nil {
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
	durable := filepath.Join(root, "durable")
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
	if err := offerUpdate(lockPath, collection, durable, "darwin/arm64", strings.NewReader("yes\n"), &output, true); err != nil {
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

func TestOfferUpdateAutomaticallyFollowsDesiredReleaseOnNativeWindows(t *testing.T) {
	root := t.TempDir()
	collection := filepath.Join(root, "collection")
	durable := filepath.Join(root, "durable")
	repository := filepath.Join(collection, "dbx")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"VERSION": "v1.2.0\n", ".gitignore": "dist/\n", "main.go": "package main\n"} {
		if err := os.WriteFile(filepath.Join(repository, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	commit := initGitRepository(t, repository)
	archive := filepath.Join(repository, "dist", "v1.2.0", "dbx_v1.2.0_windows_amd64.zip")
	writePromotionZip(t, archive, "dbx.exe")
	lock := builtins.Lock{SchemaVersion: 2, DefaultRoot: "../agent-platform-builtins", Components: []builtins.Component{{
		Name: "dbx", Version: "v1.2.0", Repository: "dbx", Source: "https://example.invalid/dbx.git", Commit: commit, Kind: "archive", Required: true,
		Targets: map[string]builtins.Target{
			"darwin-arm64":  {Version: "v1.2.0", Source: "https://example.invalid/dbx.git", Commit: commit, Path: "dist/v1.2.0/dbx_v1.2.0_darwin_arm64.tar.gz", Format: "tar.gz", Entry: "dbx", Output: "dbx", SHA256: strings.Repeat("a", 64)},
			"windows-amd64": {Version: "v1.0.0", Source: "https://example.invalid/dbx.git", Commit: strings.Repeat("b", 40), Path: "dist/v1.0.0/dbx_v1.0.0_windows_amd64.zip", Format: "zip", Entry: "dbx.exe", Output: "dbx.exe", SHA256: strings.Repeat("c", 64)},
		},
	}}}
	lockPath := writePromotionLock(t, root, lock)
	var output bytes.Buffer
	if err := offerUpdate(lockPath, collection, durable, "windows/amd64", strings.NewReader(""), &output, false); err != nil {
		t.Fatal(err)
	}
	updated, err := builtins.LoadLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	target := updated.Components[0].Targets["windows-amd64"]
	if target.Version != "v1.2.0" || target.Commit != commit || !strings.Contains(output.String(), "automatically follow") || strings.Contains(output.String(), "Type yes") {
		t.Fatalf("follower target/output = %#v / %q", target, output.String())
	}
	if got := updated.Components[0].Targets["darwin-arm64"].SHA256; got != strings.Repeat("a", 64) {
		t.Fatalf("Darwin SHA changed to %s", got)
	}
}

func TestOfferUpdateRejectsFollowerCheckoutMismatch(t *testing.T) {
	root := t.TempDir()
	collection := filepath.Join(root, "collection")
	durable := filepath.Join(root, "durable")
	repository := filepath.Join(collection, "dbx")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "VERSION"), []byte("v1.2.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = initGitRepository(t, repository)
	lock := builtins.Lock{SchemaVersion: 2, DefaultRoot: "../agent-platform-builtins", Components: []builtins.Component{{
		Name: "dbx", Version: "v1.2.0", Repository: "dbx", Source: "https://example.invalid/dbx.git", Commit: strings.Repeat("f", 40), Kind: "archive", Required: true,
		Targets: map[string]builtins.Target{"darwin-arm64": {Version: "v1.0.0", Source: "https://example.invalid/dbx.git", Commit: strings.Repeat("a", 40), Path: "dist/v1.0.0/dbx.tar.gz", Format: "tar.gz", Entry: "dbx", Output: "dbx", SHA256: strings.Repeat("b", 64)}},
	}}}
	lockPath := writePromotionLock(t, root, lock)
	before, _ := os.ReadFile(lockPath)
	var output bytes.Buffer
	if err := offerUpdate(lockPath, collection, durable, "darwin/arm64", strings.NewReader(""), &output, false); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(lockPath)
	if !bytes.Equal(before, after) || !strings.Contains(output.String(), "does not match target commit") {
		t.Fatalf("mismatch result output=%q", output.String())
	}
}

func TestOfferUpdateRejectsExistingArtifactWithDifferentSHA(t *testing.T) {
	_, lockPath, collection, durable, _ := promotionFixture(t, false)
	destination := filepath.Join(durable, "dbx", "dist", "v1.2.0", "dbx_v1.2.0_darwin_arm64.tar.gz")
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("different"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(lockPath)
	var output bytes.Buffer
	err := offerUpdate(lockPath, collection, durable, "darwin/arm64", strings.NewReader("yes\n"), &output, true)
	if err == nil || !strings.Contains(err.Error(), "different SHA-256") {
		t.Fatalf("error = %v", err)
	}
	after, _ := os.ReadFile(lockPath)
	if !bytes.Equal(before, after) {
		t.Fatal("lock changed after durable artifact conflict")
	}
}

func TestOfferUpdateNewerLeaderPreemptsIncompleteRollout(t *testing.T) {
	_, lockPath, collection, durable, localCommit := promotionFixture(t, false)
	lock, err := builtins.LoadLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	lock.SchemaVersion = 2
	component := &lock.Components[0]
	component.Version = "v1.1.0"
	component.Commit = strings.Repeat("d", 40)
	darwin := component.Targets["darwin-arm64"]
	darwin.Version = "v1.1.0"
	darwin.Commit = strings.Repeat("d", 40)
	darwin.Path = "dist/v1.1.0/dbx_v1.1.0_darwin_arm64.tar.gz"
	darwin.SHA256 = strings.Repeat("e", 64)
	component.Targets["darwin-arm64"] = darwin
	payload, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, append(payload, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := offerUpdate(lockPath, collection, durable, "darwin/arm64", strings.NewReader("yes\n"), &output, true); err != nil {
		t.Fatal(err)
	}
	updated, err := builtins.LoadLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	component = &updated.Components[0]
	if component.Version != "v1.2.0" || component.Commit != localCommit {
		t.Fatalf("preempted component = %#v", component)
	}
	if windows := component.Targets["windows-amd64"]; windows.Version != "v1.0.0" || windows.SHA256 != strings.Repeat("c", 64) {
		t.Fatalf("lagging Windows target changed = %#v", windows)
	}
	if !strings.Contains(output.String(), "v1.1.0 -> v1.2.0") {
		t.Fatalf("output = %q", output.String())
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

func promotionFixture(t *testing.T, dirty bool) (string, string, string, string, string) {
	t.Helper()
	root := t.TempDir()
	collection := filepath.Join(root, "collection")
	durable := filepath.Join(root, "durable")
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
		Targets: map[string]builtins.Target{
			"darwin-arm64":  {Path: "dist/v1.0.0/dbx_v1.0.0_darwin_arm64.tar.gz", Format: "tar.gz", Entry: "dbx", Output: "dbx", SHA256: strings.Repeat("b", 64)},
			"windows-amd64": {Path: "dist/v1.0.0/dbx_v1.0.0_windows_amd64.zip", Format: "zip", Entry: "dbx.exe", Output: "dbx.exe", SHA256: strings.Repeat("c", 64)},
		},
	}}}
	lockPath := writePromotionLock(t, root, lock)
	return root, lockPath, collection, durable, localCommit
}

func writePromotionLock(t *testing.T, root string, lock builtins.Lock) string {
	t.Helper()
	lockPath := filepath.Join(root, "builtins.lock.json")
	payload, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, append(payload, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return lockPath
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

func writePromotionZip(t *testing.T, archivePath, entry string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	item, err := writer.Create(entry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := item.Write([]byte("binary")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
