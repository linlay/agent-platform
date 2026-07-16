package builtins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestStageCacheCopiesVerifiedTargetCache(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "build", "builtins", "darwin-arm64")
	outputDir := filepath.Join(t.TempDir(), "bundle")
	rgPath := filepath.Join(cacheDir, "bin", "rg")
	launcherPath := filepath.Join(cacheDir, "bin", "pdftotext")
	runtimeRoot := filepath.Join(cacheDir, "libexec", "poppler-pdftotext", "darwin-arm64")
	mustWrite(t, rgPath, []byte("rg-binary"))
	mustWrite(t, launcherPath, []byte("launcher"))
	mustWrite(t, filepath.Join(runtimeRoot, "bin", "pdftotext"), []byte("runtime"))
	mustWrite(t, filepath.Join(cacheDir, "licenses", "rg", "LICENSE-MIT"), []byte("license"))
	mustWrite(t, filepath.Join(cacheDir, "sbom", "kbase-lance-engine.cdx.json"), []byte("{}\n"))

	tree := []TreeOutput{
		{Path: "bin/pdftotext", Type: "file"},
		{Path: "libexec/poppler-pdftotext/darwin-arm64", Type: "dir"},
	}
	treeDigest, err := TreeDigest(cacheDir, tree)
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		SchemaVersion: lockSchemaVersion,
		Platform:      ManifestPlatform{OS: "darwin", Arch: "arm64"},
		Components: []ManifestComponent{
			{Name: "rg", Version: "15.1.0", Path: "bin/rg", SHA256: fileSHA256(t, rgPath)},
			{Name: "poppler-pdftotext", Version: "v1", Path: "bin/pdftotext", SHA256: treeDigest, Tree: tree},
		},
	}
	writeCacheManifest(t, filepath.Join(cacheDir, "builtins.manifest.json"), manifest)

	result, err := StageCache(CacheStageOptions{CacheDir: cacheDir, OutputDir: outputDir, GOOS: "darwin", GOARCH: "arm64"})
	if err != nil {
		t.Fatalf("StageCache: %v", err)
	}
	if result.CacheDir != cacheDir {
		t.Fatalf("cache dir = %q, want %q", result.CacheDir, cacheDir)
	}
	for _, relativePath := range []string{
		"bin/rg",
		"bin/pdftotext",
		"libexec/poppler-pdftotext/darwin-arm64/bin/pdftotext",
		"licenses/rg/LICENSE-MIT",
		"sbom/kbase-lance-engine.cdx.json",
		"builtins.manifest.json",
	} {
		if _, err := os.Stat(filepath.Join(outputDir, filepath.FromSlash(relativePath))); err != nil {
			t.Fatalf("copied %s: %v", relativePath, err)
		}
	}
	if err := VerifyManifest(outputDir, result.Manifest); err != nil {
		t.Fatalf("VerifyManifest copied output: %v", err)
	}
}

func TestStageCacheRejectsInvalidCache(t *testing.T) {
	t.Run("overlapping output", func(t *testing.T) {
		cacheDir := newSingleFileCache(t, "darwin", "arm64")
		_, err := StageCache(CacheStageOptions{CacheDir: cacheDir, OutputDir: filepath.Join(cacheDir, "bundle"), GOOS: "darwin", GOARCH: "arm64"})
		if err == nil || !strings.Contains(err.Error(), "must not overlap") {
			t.Fatalf("StageCache error = %v, want overlapping-path rejection", err)
		}
	})

	t.Run("platform mismatch", func(t *testing.T) {
		cacheDir := newSingleFileCache(t, "darwin", "arm64")
		_, err := StageCache(CacheStageOptions{CacheDir: cacheDir, OutputDir: filepath.Join(t.TempDir(), "bundle"), GOOS: "linux", GOARCH: "arm64"})
		if err == nil || !strings.Contains(err.Error(), "does not match target") {
			t.Fatalf("StageCache error = %v, want platform mismatch", err)
		}
	})

	t.Run("modified payload", func(t *testing.T) {
		cacheDir := newSingleFileCache(t, "darwin", "arm64")
		mustWrite(t, filepath.Join(cacheDir, "bin", "rg"), []byte("modified"))
		_, err := StageCache(CacheStageOptions{CacheDir: cacheDir, OutputDir: filepath.Join(t.TempDir(), "bundle"), GOOS: "darwin", GOARCH: "arm64"})
		if err == nil || !containsSHA256Mismatch(err.Error()) {
			t.Fatalf("StageCache error = %v, want SHA-256 mismatch", err)
		}
	})

	t.Run("modified tree", func(t *testing.T) {
		cacheDir := filepath.Join(t.TempDir(), "darwin-arm64")
		launcherPath := filepath.Join(cacheDir, "bin", "pdftotext")
		runtimePath := filepath.Join(cacheDir, "libexec", "poppler-pdftotext", "darwin-arm64", "bin", "pdftotext")
		mustWrite(t, launcherPath, []byte("launcher"))
		mustWrite(t, runtimePath, []byte("runtime"))
		tree := []TreeOutput{{Path: "bin/pdftotext", Type: "file"}, {Path: "libexec/poppler-pdftotext/darwin-arm64", Type: "dir"}}
		digest, err := TreeDigest(cacheDir, tree)
		if err != nil {
			t.Fatal(err)
		}
		writeCacheManifest(t, filepath.Join(cacheDir, "builtins.manifest.json"), Manifest{
			SchemaVersion: lockSchemaVersion,
			Platform:      ManifestPlatform{OS: "darwin", Arch: "arm64"},
			Components: []ManifestComponent{{
				Name: "poppler-pdftotext", Version: "v1", Path: "bin/pdftotext", SHA256: digest, Tree: tree,
			}},
		})
		mustWrite(t, runtimePath, []byte("modified runtime"))
		_, err = StageCache(CacheStageOptions{CacheDir: cacheDir, OutputDir: filepath.Join(t.TempDir(), "bundle"), GOOS: "darwin", GOARCH: "arm64"})
		if err == nil || !containsSHA256Mismatch(err.Error()) {
			t.Fatalf("StageCache error = %v, want tree SHA-256 mismatch", err)
		}
	})

	t.Run("symbolic link", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("creating symbolic links requires elevated Windows privileges")
		}
		cacheDir := newSingleFileCache(t, "darwin", "arm64")
		mustWrite(t, filepath.Join(cacheDir, "licenses", "placeholder"), []byte("license"))
		if err := os.Symlink("rg", filepath.Join(cacheDir, "licenses", "link")); err != nil {
			t.Fatal(err)
		}
		_, err := StageCache(CacheStageOptions{CacheDir: cacheDir, OutputDir: filepath.Join(t.TempDir(), "bundle"), GOOS: "darwin", GOARCH: "arm64"})
		if err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("StageCache error = %v, want symbolic link rejection", err)
		}
	})
}

func newSingleFileCache(t *testing.T, goos, goarch string) string {
	t.Helper()
	cacheDir := filepath.Join(t.TempDir(), goos+"-"+goarch)
	payloadPath := filepath.Join(cacheDir, "bin", "rg")
	mustWrite(t, payloadPath, []byte("original"))
	writeCacheManifest(t, filepath.Join(cacheDir, "builtins.manifest.json"), Manifest{
		SchemaVersion: lockSchemaVersion,
		Platform:      ManifestPlatform{OS: goos, Arch: goarch},
		Components: []ManifestComponent{{
			Name: "rg", Version: "15.1.0", Path: "bin/rg", SHA256: fileSHA256(t, payloadPath),
		}},
	})
	return cacheDir
}

func writeCacheManifest(t *testing.T, path string, manifest Manifest) {
	t.Helper()
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, path, append(payload, '\n'))
}

func containsSHA256Mismatch(value string) bool {
	return strings.Contains(strings.ToLower(strings.ReplaceAll(value, "-", "")), "sha256 mismatch")
}
