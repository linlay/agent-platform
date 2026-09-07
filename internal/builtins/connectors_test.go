package builtins

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"agent-platform/internal/connector"
	"agent-platform/internal/resources"
)

func TestBuiltinConnectorBundleLoadsWithoutRuntimeInstall(t *testing.T) {
	cache := t.TempDir()
	entry := "dbx"
	if runtime.GOOS == "windows" {
		entry += ".exe"
	}
	mustWrite(t, filepath.Join(cache, "bin", entry), []byte("locked-executable-v1"))
	manifest := Manifest{SchemaVersion: manifestSchemaVersion, Platform: ManifestPlatform{OS: runtime.GOOS, Arch: runtime.GOARCH}, Components: []ManifestComponent{{Name: "dbx", Version: "1.0.0", Path: "bin/" + entry, SHA256: fileSHA256(t, filepath.Join(cache, "bin", entry))}}}
	writeCacheManifest(t, filepath.Join(cache, "builtins.manifest.json"), manifest)
	bundle := t.TempDir()
	result, err := StageCache(CacheStageOptions{CacheDir: cache, OutputDir: bundle, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH})
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyManifest(bundle, result.Manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(bundle, "bin", entry)); !os.IsNotExist(err) {
		t.Fatal("connector present in global bin")
	}
	setProcessBinDirForTest(t, filepath.Join(bundle, "bin"))
	root, err := ProcessConnectorsRoot()
	if err != nil || root != filepath.Join(bundle, "connectors") {
		t.Fatalf("bundle root %q: %v", root, err)
	}
	external := t.TempDir()
	sources := connector.Sources{BuiltinRoot: root, ExternalRoot: external}
	pkg, err := sources.Load("builtin.dbx")
	if err != nil || !pkg.Builtin || len(pkg.Skills) != 1 {
		t.Fatalf("package: %#v %v", pkg, err)
	}
	for _, file := range []string{"SKILL.md", "references/commands.md", "assets/dbx.png"} {
		relative := "skills/builtin-dbx/" + file
		got, err := os.ReadFile(filepath.Join(pkg.Dir, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		want, err := resources.ConnectorFS.ReadFile("connectors/builtin.dbx/" + relative)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("bundled %s differs from source: %v", file, err)
		}
	}
	entries, err := os.ReadDir(external)
	if err != nil || len(entries) != 0 {
		t.Fatalf("runtime modified: %v %v", entries, err)
	}
	mustWrite(t, filepath.Join(pkg.Dir, "skills", "builtin-dbx", "references", "commands.md"), []byte("tampered"))
	if _, err := ProcessConnectorsRoot(); err == nil {
		t.Fatal("tampered reference accepted")
	}
}

func TestStageCacheRefreshesSkillsWithoutRebuildingExecutable(t *testing.T) {
	cache := t.TempDir()
	relative := "connectors/builtin.httpx"
	dir := filepath.Join(cache, filepath.FromSlash(relative))
	if err := connector.WriteBuiltin(dir, "httpx", "0.1.8", runtime.GOOS); err != nil {
		t.Fatal(err)
	}
	entry := "httpx"
	if runtime.GOOS == "windows" {
		entry += ".exe"
	}
	binary := []byte("verified native executable")
	mustWrite(t, filepath.Join(dir, "bin", entry), binary)
	mustWrite(t, filepath.Join(dir, "skills", "httpx", "SKILL.md"), []byte("old generated skill"))
	mustWrite(t, filepath.Join(dir, "skills", "builtin-httpx", "SKILL.md"), []byte("old skill version"))
	outputs := []TreeOutput{{Path: relative, Type: "dir"}}
	digest, err := TreeDigest(cache, outputs)
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{SchemaVersion: manifestSchemaVersion, Platform: ManifestPlatform{OS: runtime.GOOS, Arch: runtime.GOARCH}, Components: []ManifestComponent{{Name: "httpx", Version: "0.1.8", Path: relative, Tree: outputs, SHA256: digest}}}
	writeCacheManifest(t, filepath.Join(cache, "builtins.manifest.json"), manifest)
	bundle := t.TempDir()
	result, err := StageCache(CacheStageOptions{CacheDir: cache, OutputDir: bundle, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH})
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyManifest(bundle, result.Manifest); err != nil {
		t.Fatal(err)
	}
	pkg, err := connector.Load(filepath.Join(bundle, "connectors"), "builtin.httpx")
	if err != nil || len(pkg.Skills) != 1 || pkg.Skills[0].Name != "builtin-httpx" {
		t.Fatalf("refreshed skills: %#v %v", pkg.Skills, err)
	}
	data, err := os.ReadFile(filepath.Join(pkg.BinDir, entry))
	if err != nil || !bytes.Equal(data, binary) {
		t.Fatalf("native binary changed: %v", err)
	}
	if result.Manifest.Components[0].SHA256 == digest {
		t.Fatal("skill update did not change package digest")
	}
	if err := VerifyManifest(cache, manifest); err != nil {
		t.Fatalf("source cache modified: %v", err)
	}
}
