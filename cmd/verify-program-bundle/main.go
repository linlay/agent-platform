package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"agent-platform/internal/builtins"
)

const (
	bundleRootName = "agent-platform"
	sidecarName    = "kbase-lance-engine"
	engineSDK      = "lancedb=0.30.0"
	popplerName    = "poppler-pdftotext"
)

type programManifest struct {
	Platform builtins.ManifestPlatform `json:"platform"`
	Runtime  struct {
		RequiredPaths []string `json:"requiredPaths"`
	} `json:"runtime"`
}

func main() {
	archivePath := flag.String("archive", "", "program bundle archive to verify")
	targetOS := flag.String("os", runtime.GOOS, "target operating system")
	targetArch := flag.String("arch", runtime.GOARCH, "target architecture")
	flag.Parse()

	if err := verifyArchive(*archivePath, *targetOS, *targetArch); err != nil {
		fmt.Fprintf(os.Stderr, "verify program bundle: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("verified program bundle %s for %s/%s\n", *archivePath, *targetOS, *targetArch)
}

func verifyArchive(archivePath, targetOS, targetArch string) error {
	if strings.TrimSpace(archivePath) == "" {
		return errors.New("--archive is required")
	}
	if err := validateTarget(targetOS, targetArch); err != nil {
		return err
	}
	tempDir, err := os.MkdirTemp("", "agent-platform-bundle-verify-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)

	if err := extractArchive(archivePath, tempDir); err != nil {
		return err
	}
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		return err
	}
	if len(entries) != 1 || !entries[0].IsDir() || entries[0].Name() != bundleRootName {
		return fmt.Errorf("archive must contain exactly one top-level %q directory", bundleRootName)
	}
	return verifyBundleRoot(filepath.Join(tempDir, bundleRootName), targetOS, targetArch)
}

func verifyBundleRoot(root, targetOS, targetArch string) error {
	manifest, err := readProgramManifest(filepath.Join(root, "manifest.json"))
	if err != nil {
		return err
	}
	if manifest.Platform.OS != targetOS || manifest.Platform.Arch != targetArch {
		return fmt.Errorf("program manifest platform %s/%s does not match target %s/%s", manifest.Platform.OS, manifest.Platform.Arch, targetOS, targetArch)
	}

	binaryName := sidecarName
	if targetOS == "windows" {
		binaryName += ".exe"
	}
	sidecarRelativePath := filepath.ToSlash(filepath.Join("bin", binaryName))
	if !containsCleanPath(manifest.Runtime.RequiredPaths, sidecarRelativePath) {
		return fmt.Errorf("program manifest runtime.requiredPaths is missing %q", sidecarRelativePath)
	}
	for _, requiredPath := range manifest.Runtime.RequiredPaths {
		clean, err := cleanRelativePath(requiredPath)
		if err != nil {
			return fmt.Errorf("invalid runtime.requiredPaths entry %q: %w", requiredPath, err)
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(clean))); err != nil {
			return fmt.Errorf("required bundle path %q: %w", clean, err)
		}
	}

	sidecarPath := filepath.Join(root, filepath.FromSlash(sidecarRelativePath))
	info, err := os.Stat(sidecarPath)
	if err != nil {
		return fmt.Errorf("sidecar %q: %w", sidecarRelativePath, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("sidecar %q is not a regular file", sidecarRelativePath)
	}
	if targetOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("sidecar %q is not executable", sidecarRelativePath)
	}

	builtinManifest, err := readBuiltinsManifest(filepath.Join(root, "builtins.manifest.json"))
	if err != nil {
		return err
	}
	if builtinManifest.Platform.OS != targetOS || builtinManifest.Platform.Arch != targetArch {
		return fmt.Errorf("builtins manifest platform %s/%s does not match target %s/%s", builtinManifest.Platform.OS, builtinManifest.Platform.Arch, targetOS, targetArch)
	}
	if err := builtins.VerifyManifest(root, builtinManifest); err != nil {
		return err
	}
	component, err := findSidecarComponent(builtinManifest)
	if err != nil {
		return err
	}
	if filepath.ToSlash(component.Path) != sidecarRelativePath {
		return fmt.Errorf("builtins manifest sidecar path is %q, want %q", component.Path, sidecarRelativePath)
	}
	if component.SDKVersion != engineSDK {
		return fmt.Errorf("builtins manifest sidecar sdkVersion is %q, want %q", component.SDKVersion, engineSDK)
	}
	actualSHA, err := fileSHA256(sidecarPath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(component.SHA256, actualSHA) {
		return fmt.Errorf("sidecar SHA-256 mismatch: manifest=%s actual=%s", component.SHA256, actualSHA)
	}
	if popplerBuiltinRequired(targetOS, targetArch) {
		if err := verifyPopplerBuiltin(root, builtinManifest, targetOS, targetArch); err != nil {
			return err
		}
	}

	for _, relativePath := range []string{
		"licenses/kbase-lance-engine/LICENSE-APACHE-2.0",
		"licenses/kbase-lance-engine/NOTICE",
		"licenses/kbase-lance-engine/THIRD_PARTY_COMPONENTS.json",
		"sbom/kbase-lance-engine.cdx.json",
	} {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(relativePath)))
		if err != nil || !info.Mode().IsRegular() {
			if err == nil {
				err = errors.New("not a regular file")
			}
			return fmt.Errorf("required sidecar release metadata %q: %w", relativePath, err)
		}
	}
	return nil
}

func popplerBuiltinRequired(targetOS, targetArch string) bool {
	return (targetOS == "darwin" && targetArch == "arm64") || (targetOS == "windows" && targetArch == "amd64")
}

func verifyPopplerBuiltin(root string, manifest builtins.Manifest, targetOS, targetArch string) error {
	component, err := findBuiltinComponent(manifest, popplerName)
	if err != nil {
		return err
	}
	launcher := "bin/pdftotext"
	if targetOS == "windows" {
		launcher += ".exe"
	}
	runtimeRoot := filepath.ToSlash(filepath.Join("libexec", popplerName, targetOS+"-"+targetArch))
	wantTree := []builtins.TreeOutput{
		{Path: launcher, Type: "file"},
		{Path: runtimeRoot, Type: "dir"},
	}
	if !sameTreeOutputs(component.Tree, wantTree) {
		return fmt.Errorf("builtins manifest %s tree = %#v, want %#v", popplerName, component.Tree, wantTree)
	}
	digest, err := builtins.TreeDigest(root, component.Tree)
	if err != nil {
		return fmt.Errorf("verify %s tree: %w", popplerName, err)
	}
	if !strings.EqualFold(component.SHA256, digest) {
		return fmt.Errorf("%s tree SHA-256 mismatch: manifest=%s actual=%s", popplerName, component.SHA256, digest)
	}
	return nil
}

func sameTreeOutputs(left, right []builtins.TreeOutput) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func readProgramManifest(path string) (programManifest, error) {
	var manifest programManifest
	if err := readJSON(path, &manifest); err != nil {
		return manifest, fmt.Errorf("read program manifest: %w", err)
	}
	if len(manifest.Runtime.RequiredPaths) == 0 {
		return manifest, errors.New("program manifest runtime.requiredPaths is empty")
	}
	return manifest, nil
}

func readBuiltinsManifest(path string) (builtins.Manifest, error) {
	var manifest builtins.Manifest
	if err := readJSON(path, &manifest); err != nil {
		return manifest, fmt.Errorf("read builtins manifest: %w", err)
	}
	return manifest, nil
}

func readJSON(path string, value any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	return decoder.Decode(value)
}

func findSidecarComponent(manifest builtins.Manifest) (builtins.ManifestComponent, error) {
	return findBuiltinComponent(manifest, sidecarName)
}

func findBuiltinComponent(manifest builtins.Manifest, name string) (builtins.ManifestComponent, error) {
	for _, component := range manifest.Components {
		if component.Name == name {
			return component, nil
		}
	}
	return builtins.ManifestComponent{}, fmt.Errorf("builtins manifest does not contain %s", name)
}

func containsCleanPath(paths []string, expected string) bool {
	for _, candidate := range paths {
		clean, err := cleanRelativePath(candidate)
		if err == nil && clean == expected {
			return true
		}
	}
	return false
}

func cleanRelativePath(value string) (string, error) {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	clean := filepath.ToSlash(filepath.Clean(value))
	if value == "" || clean == "." || strings.HasPrefix(clean, "../") || clean == ".." || filepath.IsAbs(value) {
		return "", errors.New("path must stay inside the bundle")
	}
	return clean, nil
}

func validateTarget(targetOS, targetArch string) error {
	if targetOS != "darwin" && targetOS != "linux" && targetOS != "windows" {
		return fmt.Errorf("unsupported target OS %q", targetOS)
	}
	if targetArch != "amd64" && targetArch != "arm64" {
		return fmt.Errorf("unsupported target architecture %q", targetArch)
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func extractArchive(archivePath, destination string) error {
	if strings.HasSuffix(strings.ToLower(archivePath), ".zip") {
		return extractZip(archivePath, destination)
	}
	if strings.HasSuffix(strings.ToLower(archivePath), ".tar.gz") || strings.HasSuffix(strings.ToLower(archivePath), ".tgz") {
		return extractTarGz(archivePath, destination)
	}
	return fmt.Errorf("unsupported archive format: %s", archivePath)
}

func extractTarGz(archivePath, destination string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		relativePath, err := cleanRelativePath(header.Name)
		if err != nil {
			return fmt.Errorf("unsafe archive entry %q: %w", header.Name, err)
		}
		targetPath := filepath.Join(destination, filepath.FromSlash(relativePath))
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, os.FileMode(header.Mode).Perm()); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := writeArchiveFile(targetPath, os.FileMode(header.Mode).Perm(), reader); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported archive entry type for %q", header.Name)
		}
	}
}

func extractZip(archivePath, destination string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer reader.Close()
	for _, entry := range reader.File {
		relativePath, err := cleanRelativePath(entry.Name)
		if err != nil {
			return fmt.Errorf("unsafe archive entry %q: %w", entry.Name, err)
		}
		targetPath := filepath.Join(destination, filepath.FromSlash(relativePath))
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(targetPath, 0o755); err != nil {
				return err
			}
			continue
		}
		input, err := entry.Open()
		if err != nil {
			return err
		}
		err = writeArchiveFile(targetPath, entry.Mode().Perm(), input)
		input.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func writeArchiveFile(path string, mode os.FileMode, input io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if mode == 0 {
		mode = 0o644
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, input); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
