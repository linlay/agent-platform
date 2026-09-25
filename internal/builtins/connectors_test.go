package builtins

import (
	"agent-platform/internal/connectortest"
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agent-platform/internal/connector"
)

func TestBuiltinConnectorManifestVersionComparison(t *testing.T) {
	for _, tc := range []struct {
		name, component, bundleVersion, connectorVersion string
		wantMismatch                                     bool
	}{
		{"dbx_release_tag", "dbx", "v0.1.2", "0.1.2", false},
		{"httpx_release_tag", "httpx", "v0.1.8", "0.1.8", false},
		{"plain_version", "dbx", "0.1.2", "0.1.2", false},
		{"prerelease_tag", "dbx", "v0.1.2-beta.1", "0.1.2-beta.1", false},
		{"different_release", "dbx", "v0.1.3", "0.1.2", true},
		{"different_prerelease", "dbx", "v0.1.2-beta.2", "0.1.2-beta.1", true},
		{"different_build", "dbx", "v0.1.2+build.2", "0.1.2+build.1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bundle := t.TempDir()
			relative := "connectors/builtin." + tc.component
			dir := filepath.Join(bundle, filepath.FromSlash(relative))
			if err := connectortest.WriteCLI(dir, tc.component, tc.connectorVersion, runtime.GOOS); err != nil {
				t.Fatal(err)
			}
			outputs := []TreeOutput{{Path: relative, Type: "dir"}}
			digest, err := TreeDigest(bundle, outputs)
			if err != nil {
				t.Fatal(err)
			}
			manifest := Manifest{
				SchemaVersion: manifestSchemaVersion,
				Platform:      ManifestPlatform{OS: runtime.GOOS, Arch: runtime.GOARCH},
				Components: []ManifestComponent{{
					Name: tc.component, Version: tc.bundleVersion,
					Path: relative, Tree: outputs, SHA256: digest,
				}},
			}

			writeCacheManifest(t, filepath.Join(bundle, "builtins.manifest.json"), manifest)
			setProcessBinDirForTest(t, filepath.Join(bundle, "bin"))
			root, err := ProcessConnectorsRoot()
			if tc.wantMismatch {
				if err == nil || !strings.Contains(err.Error(), "manifest version mismatch") || !strings.Contains(err.Error(), tc.bundleVersion) || !strings.Contains(err.Error(), tc.connectorVersion) {
					t.Fatalf("expected both mismatched versions in diagnostic, got root=%q err=%v", root, err)
				}
				return
			}
			if err != nil || root != filepath.Join(bundle, "connectors") {
				t.Fatalf("matching release rejected: root=%q err=%v", root, err)
			}
		})
	}
}

func TestBuiltinConnectorBundleLoadsWithoutRuntimeInstall(t *testing.T) {
	cache := t.TempDir()
	entry := "dbx"
	if runtime.GOOS == "windows" {
		entry += ".exe"
	}
	dir := filepath.Join(cache, "connectors", "builtin.dbx")
	if err := connectortest.WriteCLI(dir, "dbx", "1.0.0", runtime.GOOS); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "bin", entry), []byte("locked-executable-v1"))
	outputs := []TreeOutput{{Path: "connectors/builtin.dbx", Type: "dir"}}
	digest, err := TreeDigest(cache, outputs)
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{SchemaVersion: manifestSchemaVersion, Platform: ManifestPlatform{OS: runtime.GOOS, Arch: runtime.GOARCH}, Components: []ManifestComponent{{Name: "dbx", Version: "1.0.0", Path: "connectors/builtin.dbx", Tree: outputs, SHA256: digest}}}
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
	for _, file := range []string{"SKILL.md", "references/commands.md"} {
		relative := "skills/builtin-dbx/" + file
		got, err := os.ReadFile(filepath.Join(pkg.Dir, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(relative)))
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

func TestStageCachePreservesCompletePackageAndDigest(t *testing.T) {
	cache := t.TempDir()
	relative := "connectors/builtin.httpx"
	dir := filepath.Join(cache, filepath.FromSlash(relative))
	if err := connectortest.WriteCLI(dir, "httpx", "0.1.8", runtime.GOOS); err != nil {
		t.Fatal(err)
	}
	entry := "httpx"
	if runtime.GOOS == "windows" {
		entry += ".exe"
	}
	binary := []byte("verified native executable")
	mustWrite(t, filepath.Join(dir, "bin", entry), binary)

	mustWrite(t, filepath.Join(dir, "skills", "builtin-httpx", "SKILL.md"), []byte("---\nname: builtin-httpx\ndescription: Locked package instructions\n---\nCustom released instructions\n"))
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
	if result.Manifest.Components[0].SHA256 != digest {
		t.Fatal("immutable package digest changed during staging")
	}
	if err := VerifyManifest(cache, manifest); err != nil {
		t.Fatalf("source cache modified: %v", err)
	}
}

func TestLegacyDesktopCacheVerifiedButExcludedFromRelease(t *testing.T) {
	cache := t.TempDir()
	if err := connectortest.WriteCLI(filepath.Join(cache, "connectors", "builtin.dbx"), "dbx", "1.0.0", runtime.GOOS); err != nil {
		t.Fatal(err)
	}
	if err := connector.WriteBuiltin(filepath.Join(cache, "connectors", "builtin.desktop"), "desktop", "", runtime.GOOS); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{SchemaVersion: manifestSchemaVersion, Platform: ManifestPlatform{OS: runtime.GOOS, Arch: runtime.GOARCH}}
	for _, name := range []string{"dbx", "desktop"} {
		path := "connectors/builtin." + name
		tree := []TreeOutput{{Path: path, Type: "dir"}}
		sum, err := TreeDigest(cache, tree)
		if err != nil {
			t.Fatal(err)
		}
		manifest.Components = append(manifest.Components, ManifestComponent{Name: name, Version: "1.0.0", Path: path, Tree: tree, SHA256: sum})
	}
	writeCacheManifest(t, filepath.Join(cache, "builtins.manifest.json"), manifest)
	setProcessBinDirForTest(t, filepath.Join(cache, "bin"))
	if _, err := ProcessConnectorsRoot(); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	staged, err := StageCache(CacheStageOptions{CacheDir: cache, OutputDir: out, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH})
	if err != nil {
		t.Fatal(err)
	}
	if len(staged.Manifest.Components) != 1 || staged.Manifest.Components[0].Name != "dbx" {
		t.Fatalf("Desktop leaked into external manifest: %+v", staged.Manifest)
	}
	if _, err := os.Stat(filepath.Join(out, "connectors", "builtin.desktop")); !os.IsNotExist(err) {
		t.Fatalf("obsolete payload: %v", err)
	}
	if err := VerifyManifest(cache, manifest); err != nil {
		t.Fatalf("source modified: %v", err)
	}
	mustWrite(t, filepath.Join(cache, "connectors", "builtin.desktop", "skills", "desktop-action", "SKILL.md"), []byte("tampered"))
	if _, err := ProcessConnectorsRoot(); err == nil {
		t.Fatal("legacy Desktop checksum bypassed")
	}
}

func TestStageCacheRejectsLegacyExecutableOnlyConnector(t *testing.T) {
	cache := t.TempDir()
	mustWrite(t, filepath.Join(cache, "bin", "dbx"), []byte("legacy"))
	m := Manifest{SchemaVersion: 1, Platform: ManifestPlatform{OS: "darwin", Arch: "arm64"}, Components: []ManifestComponent{{Name: "dbx", Version: "1.0.0", Path: "bin/dbx", SHA256: fileSHA256(t, filepath.Join(cache, "bin", "dbx"))}}}
	writeCacheManifest(t, filepath.Join(cache, "builtins.manifest.json"), m)
	_, err := StageCache(CacheStageOptions{CacheDir: cache, OutputDir: t.TempDir(), GOOS: "darwin", GOARCH: "arm64"})
	if err == nil || !strings.Contains(err.Error(), "complete connector package") {
		t.Fatalf("legacy cache accepted: %v", err)
	}
}

func TestStageIndependentConnectorLock(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	packageDir := filepath.Join(source, "connectors", "builtin.dbx")
	if err := connectortest.WriteCLI(packageDir, "dbx", "1.2.3", "darwin"); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(packageDir, "bin", "dbx"), []byte("versioned CLI"))
	payload := map[string][]byte{}
	err := filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		payload["runtime/"+filepath.ToSlash(rel)] = data
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	connectorsRoot := filepath.Join(root, "connector-projects")
	archive := filepath.Join(connectorsRoot, "dbx", "release.tar.gz")
	mustWriteTreeTarGzip(t, archive, payload)
	connectorLock := Lock{SchemaVersion: 2, DefaultRoot: "unused-connectors", Components: []Component{{Name: "dbx", Version: "v1.2.3", Repository: "dbx", Kind: "archive-tree", Required: true, Targets: map[string]Target{"darwin-arm64": {Version: "v1.2.3", Path: "release.tar.gz", Format: "tar.gz", SHA256: fileSHA256(t, archive), Tree: &TreeLayout{Root: "runtime", Outputs: []TreeOutput{{Path: "connectors/builtin.dbx", Type: "dir"}}}}}}}}
	writeLock(t, filepath.Join(root, "connectors.lock.json"), connectorLock)
	builtinsRoot := filepath.Join(root, "builtin-projects")
	mustWrite(t, filepath.Join(builtinsRoot, "rg", "rg"), []byte("rg"))
	builtinLock := Lock{SchemaVersion: 2, DefaultRoot: "unused-builtins", Components: []Component{{Name: "rg", Version: "1.0.0", Repository: "rg", Kind: "file", Required: true, Targets: map[string]Target{"darwin-arm64": {Version: "1.0.0", Path: "rg", Output: "rg", SHA256: fileSHA256(t, filepath.Join(builtinsRoot, "rg", "rg"))}}}}}
	writeLock(t, filepath.Join(root, "builtins.lock.json"), builtinLock)
	options := StageOptions{RepoRoot: root, LockPath: "builtins.lock.json", BuiltinsRoot: builtinsRoot, ConnectorsLockPath: "connectors.lock.json", ConnectorsRoot: connectorsRoot, OutputDir: "out", GOOS: "darwin", GOARCH: "arm64"}
	result, err := Stage(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyManifest(filepath.Join(root, "out"), result.Manifest); err != nil {
		t.Fatal(err)
	}
	if len(result.Manifest.Components) != 2 {
		t.Fatalf("missing components: %+v", result.Manifest)
	}
	for name, want := range payload {
		rel := strings.TrimPrefix(name, "runtime/")
		got, err := os.ReadFile(filepath.Join(root, "out", filepath.FromSlash(rel)))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("package content altered: %s: %v", rel, err)
		}
	}
	// Duplicate ownership is rejected before writing output.
	builtinLock.Components = connectorLock.Components
	writeLock(t, filepath.Join(root, "builtins.lock.json"), builtinLock)
	if _, err := Stage(options); err == nil || !strings.Contains(err.Error(), "duplicate component") {
		t.Fatalf("duplicate owner: %v", err)
	}
}
