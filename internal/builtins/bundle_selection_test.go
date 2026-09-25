package builtins

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBundleGitBashEnv(t *testing.T) {
	for _, value := range []string{"", "true", "TRUE", " false ", "false", "typo", "0"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("BUNDLE_GIT_BASH", value)
			got, err := BundleGitBashFromEnv()
			if value == "typo" || value == "0" {
				if err == nil {
					t.Fatal("invalid value accepted")
				}
				return
			}
			want := value != "false" && value != " false "
			if err != nil || got != want {
				t.Fatalf("got %v, %v", got, err)
			}
		})
	}
}

func TestBundleGitBashDoesNotChangeMacCache(t *testing.T) {
	cache := newSingleFileCache(t, "darwin", "arm64")
	for _, exclude := range []bool{false, true} {
		result, err := StageCache(CacheStageOptions{CacheDir: cache, OutputDir: t.TempDir(), GOOS: "darwin", GOARCH: "arm64", ExcludeGitBash: exclude})
		if err != nil {
			t.Fatal(err)
		}
		if result.Manifest.GitBashExcluded {
			t.Fatal("Windows selection leaked into macOS manifest")
		}
		if len(result.Manifest.Components) != 1 {
			t.Fatal("macOS component selection changed")
		}
	}
}

func TestBundleGitBashExcludedCacheRoundTrip(t *testing.T) {
	cache := newSingleFileCache(t, "windows", "amd64")
	m, err := LoadManifest(filepath.Join(cache, "builtins.manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Even a broken/unreadable excluded component must not be opened or copied.
	m.Components = append(m.Components, ManifestComponent{Name: GitBashComponent, Path: GitBashRelativeRoot, SHA256: "invalid"})
	for _, dir := range []string{"libexec", "licenses", "sbom"} {
		mustWrite(t, filepath.Join(cache, dir, GitBashComponent, "keep-in-cache"), []byte("source"))
	}
	writeCacheManifest(t, filepath.Join(cache, "builtins.manifest.json"), m)
	out := t.TempDir()
	mustWrite(t, filepath.Join(out, GitBashRelativeRoot, "stale"), []byte("stale"))
	result, err := StageCache(CacheStageOptions{CacheDir: cache, OutputDir: out, GOOS: "windows", GOARCH: "amd64", ExcludeGitBash: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Manifest.GitBashExcluded || len(result.Manifest.Components) != 1 {
		t.Fatal("exclusion missing")
	}
	if err := VerifyPlatformSelection(out, result.Manifest); err != nil {
		t.Fatal(err)
	}
	if err := VerifyManifest(out, result.Manifest); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"libexec", "licenses", "sbom"} {
		if _, err := os.Stat(filepath.Join(cache, dir, GitBashComponent, "keep-in-cache")); err != nil {
			t.Fatal("source cache changed", err)
		}
	}
	// Default true must not inherit the previous cache's intentional exclusion.
	if _, err := StageCache(CacheStageOptions{CacheDir: out, OutputDir: t.TempDir(), GOOS: "windows", GOARCH: "amd64"}); err == nil {
		t.Fatal("enabled release accepted excluded cache")
	}
	if _, err := verifyGitBashAt(out); err == nil {
		t.Fatal("runtime enabled without Git Bash accepted")
	}
	if _, err := StageCache(CacheStageOptions{CacheDir: out, OutputDir: t.TempDir(), GOOS: "windows", GOARCH: "amd64", ExcludeGitBash: true}); err != nil {
		t.Fatal(err)
	}
	result.Manifest.Components = append(result.Manifest.Components, ManifestComponent{Name: GitBashComponent})
	if err := RequirePlatformComponents(result.Manifest); err == nil {
		t.Fatal("contradictory manifest accepted")
	}
}
