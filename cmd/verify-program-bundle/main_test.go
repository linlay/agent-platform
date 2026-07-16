package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/builtins"
)

func TestVerifyArchiveAcceptsCompleteBundles(t *testing.T) {
	for _, test := range []struct {
		name   string
		goos   string
		goarch string
		ext    string
		create func(*testing.T, string, string)
	}{
		{name: "darwin tar.gz", goos: "darwin", goarch: "arm64", ext: ".tar.gz", create: createTarGz},
		{name: "windows zip", goos: "windows", goarch: "amd64", ext: ".zip", create: createZip},
	} {
		t.Run(test.name, func(t *testing.T) {
			stagingRoot := t.TempDir()
			bundleRoot := filepath.Join(stagingRoot, bundleRootName)
			writeCompleteBundle(t, bundleRoot, test.goos, test.goarch)
			archivePath := filepath.Join(t.TempDir(), "bundle"+test.ext)
			test.create(t, stagingRoot, archivePath)
			if err := verifyArchive(archivePath, test.goos, test.goarch); err != nil {
				t.Fatalf("verifyArchive: %v", err)
			}
		})
	}
}

func TestVerifyBundleRootRejectsIncompleteRelease(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*testing.T, string)
		message string
	}{
		{
			name: "sidecar omitted from required paths",
			mutate: func(t *testing.T, root string) {
				writeProgramManifest(t, root, "darwin", "arm64", []string{"backend/agent-platform"})
			},
			message: "runtime.requiredPaths is missing",
		},
		{
			name: "sidecar file missing",
			mutate: func(t *testing.T, root string) {
				if err := os.Remove(filepath.Join(root, "bin", sidecarName)); err != nil {
					t.Fatal(err)
				}
			},
			message: "required bundle path",
		},
		{
			name: "sidecar not executable",
			mutate: func(t *testing.T, root string) {
				if err := os.Chmod(filepath.Join(root, "bin", sidecarName), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			message: "is not executable",
		},
		{
			name: "builtins digest mismatch",
			mutate: func(t *testing.T, root string) {
				manifest := readFixtureBuiltinsManifest(t, root)
				manifest.Components[0].SHA256 = strings.Repeat("0", 64)
				writeJSON(t, filepath.Join(root, "builtins.manifest.json"), manifest, 0o644)
			},
			message: "sha256 mismatch",
		},
		{
			name: "ordinary builtin digest mismatch",
			mutate: func(t *testing.T, root string) {
				manifest := readFixtureBuiltinsManifest(t, root)
				for index := range manifest.Components {
					if manifest.Components[index].Name == "rg" {
						manifest.Components[index].SHA256 = strings.Repeat("0", 64)
					}
				}
				writeJSON(t, filepath.Join(root, "builtins.manifest.json"), manifest, 0o644)
			},
			message: "component rg",
		},
		{
			name: "dependency inventory missing",
			mutate: func(t *testing.T, root string) {
				if err := os.Remove(filepath.Join(root, "licenses", sidecarName, "THIRD_PARTY_COMPONENTS.json")); err != nil {
					t.Fatal(err)
				}
			},
			message: "required sidecar release metadata",
		},
		{
			name: "sidecar sbom missing",
			mutate: func(t *testing.T, root string) {
				if err := os.Remove(filepath.Join(root, "sbom", sidecarName+".cdx.json")); err != nil {
					t.Fatal(err)
				}
			},
			message: "required sidecar release metadata",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), bundleRootName)
			writeCompleteBundle(t, root, "darwin", "arm64")
			test.mutate(t, root)
			err := verifyBundleRoot(root, "darwin", "arm64")
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("verifyBundleRoot error = %v, want message containing %q", err, test.message)
			}
		})
	}
}

func writeCompleteBundle(t *testing.T, root, goos, goarch string) {
	t.Helper()
	binaryName := sidecarName
	if goos == "windows" {
		binaryName += ".exe"
	}
	sidecarRelativePath := filepath.ToSlash(filepath.Join("bin", binaryName))
	writeFile(t, filepath.Join(root, filepath.FromSlash(sidecarRelativePath)), []byte("sidecar-binary"), 0o755)
	writeFile(t, filepath.Join(root, "backend", "agent-platform"), []byte("runtime-binary"), 0o755)
	requiredPaths := []string{"backend/agent-platform", sidecarRelativePath}
	digest, err := fileSHA256(filepath.Join(root, filepath.FromSlash(sidecarRelativePath)))
	if err != nil {
		t.Fatal(err)
	}
	components := []builtins.ManifestComponent{{
		Name:         sidecarName,
		Version:      "1.0.0",
		Path:         sidecarRelativePath,
		SHA256:       digest,
		SDKVersion:   engineSDK,
		License:      "Apache-2.0",
		Distribution: "checksum-verified-artifact",
	}}
	writeFile(t, filepath.Join(root, "bin", "rg"), []byte("rg-binary"), 0o755)
	rgDigest, err := fileSHA256(filepath.Join(root, "bin", "rg"))
	if err != nil {
		t.Fatal(err)
	}
	components = append(components, builtins.ManifestComponent{
		Name: "rg", Version: "15.1.0", Path: "bin/rg", SHA256: rgDigest,
	})
	if popplerBuiltinRequired(goos, goarch) {
		launcher := "bin/pdftotext"
		if goos == "windows" {
			launcher += ".exe"
		}
		runtimeRoot := filepath.ToSlash(filepath.Join("libexec", popplerName, goos+"-"+goarch))
		writeFile(t, filepath.Join(root, filepath.FromSlash(launcher)), []byte("launcher"), 0o755)
		writeFile(t, filepath.Join(root, filepath.FromSlash(runtimeRoot), "bin", filepath.Base(launcher)), []byte("runtime"), 0o755)
		tree := []builtins.TreeOutput{{Path: launcher, Type: "file"}, {Path: runtimeRoot, Type: "dir"}}
		treeDigest, err := builtins.TreeDigest(root, tree)
		if err != nil {
			t.Fatal(err)
		}
		components = append(components, builtins.ManifestComponent{
			Name: popplerName, Version: "v26.06.0", Path: launcher, SHA256: treeDigest, Tree: tree,
			Distribution: "checksum-verified-artifact",
		})
		requiredPaths = append(requiredPaths, launcher, runtimeRoot)
	}
	writeProgramManifest(t, root, goos, goarch, requiredPaths)
	writeJSON(t, filepath.Join(root, "builtins.manifest.json"), builtins.Manifest{
		SchemaVersion: 1,
		Platform:      builtins.ManifestPlatform{OS: goos, Arch: goarch},
		Components:    components,
	}, 0o644)
	for _, relativePath := range []string{
		"licenses/kbase-lance-engine/LICENSE-APACHE-2.0",
		"licenses/kbase-lance-engine/NOTICE",
		"licenses/kbase-lance-engine/THIRD_PARTY_COMPONENTS.json",
		"sbom/kbase-lance-engine.cdx.json",
	} {
		writeFile(t, filepath.Join(root, filepath.FromSlash(relativePath)), []byte("{}\n"), 0o644)
	}
}

func writeProgramManifest(t *testing.T, root, goos, goarch string, requiredPaths []string) {
	t.Helper()
	writeJSON(t, filepath.Join(root, "manifest.json"), map[string]any{
		"kind":     "builtin",
		"id":       bundleRootName,
		"platform": map[string]any{"os": goos, "arch": goarch},
		"runtime":  map[string]any{"requiredPaths": requiredPaths},
	}, 0o644)
}

func readFixtureBuiltinsManifest(t *testing.T, root string) builtins.Manifest {
	t.Helper()
	var manifest builtins.Manifest
	payload, err := os.ReadFile(filepath.Join(root, "builtins.manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func writeJSON(t *testing.T, path string, value any, mode os.FileMode) {
	t.Helper()
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, append(payload, '\n'), mode)
}

func writeFile(t *testing.T, path string, payload []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, mode); err != nil {
		t.Fatal(err)
	}
}

func createTarGz(t *testing.T, sourceRoot, archivePath string) {
	t.Helper()
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	err = filepath.Walk(sourceRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == sourceRoot {
			return nil
		}
		relativePath, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relativePath)
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			input, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(tarWriter, input)
			input.Close()
			return copyErr
		}
		return nil
	})
	if err == nil {
		err = tarWriter.Close()
	}
	if err == nil {
		err = gzipWriter.Close()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
}

func createZip(t *testing.T, sourceRoot, archivePath string) {
	t.Helper()
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	err = filepath.Walk(sourceRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == sourceRoot || info.IsDir() {
			return nil
		}
		relativePath, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relativePath)
		output, err := writer.CreateHeader(header)
		if err != nil {
			return err
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, input)
		input.Close()
		return copyErr
	})
	if err == nil {
		err = writer.Close()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
}
