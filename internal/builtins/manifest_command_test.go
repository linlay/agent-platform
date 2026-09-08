package builtins

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestManifestCommandSigningRoundTrip(t *testing.T) {
	root := newSingleFileCache(t, "darwin", "arm64")
	manifestPath := filepath.Join(root, "builtins.manifest.json")
	manifest, _ := LoadManifest(manifestPath)
	tree := []TreeOutput{{Path: "connectors/builtin.dbx", Type: "dir"}}
	mustWrite(t, filepath.Join(root, "connectors/builtin.dbx/bin/dbx"), []byte("original executable"))
	mustWrite(t, filepath.Join(root, "connectors/builtin.dbx/skills/SKILL.md"), []byte("skill content"))
	digest, _ := TreeDigest(root, tree)
	manifest.Components = append(manifest.Components, ManifestComponent{
		Name: "dbx", Version: "v1.0.0", Source: "source", Commit: "commit", License: "MIT",
		Path: tree[0].Path, Tree: tree, SHA256: digest,
	})
	writeCacheManifest(t, manifestPath, manifest)
	before, _ := os.ReadFile(manifestPath)
	var output bytes.Buffer
	if err := RunManifestCommand([]string{"verify", "--bundle-root", root}, &output); err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		ManifestSHA256 string `json:"manifestSha256"`
	}
	if err := json.Unmarshal(output.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.ManifestSHA256 != bytesSHA256(before) {
		t.Fatal("wrong verification receipt")
	}
	mustWrite(t, filepath.Join(root, "bin/rg"), []byte("new signature"))
	mustWrite(t, filepath.Join(root, "connectors/builtin.dbx/bin/dbx"), []byte("new connector signature"))
	if err := RunManifestCommand([]string{"verify", "--bundle-root", root}, &output); err == nil {
		t.Fatal("modified executable was accepted before refresh")
	}
	if err := RunManifestCommand([]string{"refresh-after-signing", "--bundle-root", root, "--expected-manifest-sha256", receipt.ManifestSHA256}, &output); err != nil {
		t.Fatal(err)
	}
	if err := RunManifestCommand([]string{"verify", "--bundle-root", root}, &output); err != nil {
		t.Fatal(err)
	}
	after, _ := LoadManifest(manifestPath)
	for i := range after.Components {
		if after.Components[i].SHA256 == manifest.Components[i].SHA256 {
			t.Fatal("component hash did not change")
		}
		after.Components[i].SHA256 = manifest.Components[i].SHA256
	}
	if !reflect.DeepEqual(after, manifest) {
		t.Fatal("refresh changed component metadata")
	}
	mustWrite(t, filepath.Join(root, "connectors/builtin.dbx/skills/SKILL.md"), []byte("tampered skill"))
	if err := RunManifestCommand([]string{"verify", "--bundle-root", root}, &output); err == nil {
		t.Fatal("tampered tree was accepted")
	}
}

func TestManifestCommandRejectsUnsafeRefreshWithoutWriting(t *testing.T) {
	for _, scenario := range []string{"windows", "linux", "stale receipt", "missing payload", "path escape", "invalid schema", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			if scenario == "symlink" && runtime.GOOS == "windows" {
				t.Skip("requires symlink privilege")
			}
			root := newSingleFileCache(t, "darwin", "arm64")
			p := filepath.Join(root, "builtins.manifest.json")
			m, _ := LoadManifest(p)
			switch scenario {
			case "windows", "linux":
				m.Platform.OS = scenario
			case "path escape":
				m.Components[0].Path = "../outside"
			case "invalid schema":
				m.SchemaVersion = 999
			case "missing payload":
				os.Remove(filepath.Join(root, "bin/rg"))
			case "symlink":
				os.Rename(filepath.Join(root, "bin"), filepath.Join(root, "real-bin"))
				if err := os.Symlink("real-bin", filepath.Join(root, "bin")); err != nil {
					t.Fatal(err)
				}
			}
			writeCacheManifest(t, p, m)
			before, _ := os.ReadFile(p)
			expected := bytesSHA256(before)
			if scenario == "stale receipt" {
				expected = strings.Repeat("0", 64)
			}
			var output bytes.Buffer
			if err := RunManifestCommand([]string{"refresh-after-signing", "--bundle-root", root, "--expected-manifest-sha256", expected}, &output); err == nil {
				t.Fatal("unsafe refresh accepted")
			}
			after, _ := os.ReadFile(p)
			if !bytes.Equal(before, after) {
				t.Fatal("failed refresh modified manifest")
			}
		})
	}
}

func TestManifestCommandRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"verify"}, {"verify", "--bundle-root", "relative"}, {"verify", "--unknown"}, {"verify", "--bundle-root", t.TempDir(), "extra"}} {
		if err := RunManifestCommand(args, &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
