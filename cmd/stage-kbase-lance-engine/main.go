package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"agent-platform/internal/builtins"
)

const componentName = "kbase-lance-engine"

func main() {
	repoRoot := flag.String("repo-root", ".", "agent-platform repository root")
	lockPath := flag.String("lock", "scripts/release-assets/builtins.lock.json", "builtins lock path")
	builtinsRoot := flag.String("builtins-root", "", "absolute builtins collection root")
	outputDir := flag.String("output", "release-local", "bundle root staged by cmd/stage-builtins")
	targetOS := flag.String("os", runtime.GOOS, "target operating system")
	targetArch := flag.String("arch", runtime.GOARCH, "target architecture")
	flag.Parse()

	if err := run(*repoRoot, *lockPath, *builtinsRoot, *outputDir, *targetOS, *targetArch); err != nil {
		fmt.Fprintf(os.Stderr, "stage kbase-lance-engine: %v\n", err)
		os.Exit(1)
	}
}

// run enriches the generic builtins staging output with the signed metadata
// embedded in the external KBASE sidecar release archive. The executable and
// ordinary license files are staged by cmd/stage-builtins first.
func run(repoRoot, lockPath, builtinsRoot, outputDir, targetOS, targetArch string) error {
	if err := validateTarget(targetOS, targetArch); err != nil {
		return err
	}
	repoRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(lockPath) {
		lockPath = filepath.Join(repoRoot, lockPath)
	}
	lock, err := builtins.LoadLock(lockPath)
	if err != nil {
		return err
	}
	component, err := builtins.FindComponent(lock, componentName)
	if err != nil {
		return err
	}
	if component.Kind != "archive" {
		return fmt.Errorf("component %s must be an external release archive", componentName)
	}
	target, ok := component.Targets[targetOS+"-"+targetArch]
	if !ok {
		return fmt.Errorf("component %s has no target %s/%s", componentName, targetOS, targetArch)
	}
	if target.Metadata == nil {
		return fmt.Errorf("component %s target %s/%s is missing release metadata", componentName, targetOS, targetArch)
	}

	collectionRoot, err := builtins.ResolveRoot(repoRoot, builtinsRoot, lock)
	if err != nil {
		return err
	}
	repositoryRoot, err := joinWithin(collectionRoot, component.Repository)
	if err != nil {
		return err
	}
	artifactPath, err := joinWithin(repositoryRoot, target.Path)
	if err != nil {
		return err
	}
	if err := verifySHA256(artifactPath, target.SHA256); err != nil {
		return fmt.Errorf("sidecar archive verification: %w", err)
	}

	cargoMetadata, err := builtins.ReadArchiveEntry(artifactPath, target.Format, target.Metadata.CargoMetadata)
	if err != nil {
		return fmt.Errorf("read cargo metadata: %w", err)
	}
	sbom, err := builtins.ReadArchiveEntry(artifactPath, target.Format, target.Metadata.SBOM)
	if err != nil {
		return fmt.Errorf("read sidecar SBOM: %w", err)
	}
	if !json.Valid(cargoMetadata) {
		return errors.New("Cargo metadata is not JSON")
	}
	if !json.Valid(sbom) {
		return errors.New("sidecar SBOM is not JSON")
	}

	if !filepath.IsAbs(outputDir) {
		outputDir = filepath.Join(repoRoot, outputDir)
	}
	if err := stageDependencyInventory(cargoMetadata, outputDir); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(outputDir, "sbom", "kbase-lance-engine.cdx.json"), sbom, 0o644); err != nil {
		return err
	}
	if err := requireLicenseFiles(outputDir); err != nil {
		return err
	}
	return updateManifest(outputDir, targetOS, targetArch, component)
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

func joinWithin(root, child string) (string, error) {
	if strings.TrimSpace(child) == "" {
		return "", errors.New("path is required")
	}
	path := filepath.Join(root, child)
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("path %q escapes %s", child, root)
	}
	return path, nil
}

func verifySHA256(path, expected string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, strings.TrimSpace(expected)) {
		return fmt.Errorf("SHA-256 mismatch: expected %s, got %s", expected, actual)
	}
	return nil
}

type cargoMetadataDocument struct {
	Packages []struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		License string `json:"license"`
		Source  string `json:"source"`
	} `json:"packages"`
}

type dependencyInventory struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Components    []dependencyComponent `json:"components"`
}

type dependencyComponent struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	License string `json:"license,omitempty"`
	Source  string `json:"source,omitempty"`
}

func stageDependencyInventory(cargoMetadata []byte, outputDir string) error {
	var metadata cargoMetadataDocument
	if err := json.Unmarshal(cargoMetadata, &metadata); err != nil {
		return fmt.Errorf("decode Cargo metadata: %w", err)
	}
	if len(metadata.Packages) == 0 {
		return errors.New("Cargo metadata contains no packages")
	}
	inventory := dependencyInventory{SchemaVersion: 1}
	for _, pkg := range metadata.Packages {
		inventory.Components = append(inventory.Components, dependencyComponent{
			Name:    pkg.Name,
			Version: pkg.Version,
			License: pkg.License,
			Source:  sanitizedPackageSource(pkg.Source),
		})
	}
	sort.SliceStable(inventory.Components, func(i, j int) bool {
		if inventory.Components[i].Name != inventory.Components[j].Name {
			return inventory.Components[i].Name < inventory.Components[j].Name
		}
		return inventory.Components[i].Version < inventory.Components[j].Version
	})
	payload, err := json.MarshalIndent(inventory, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return writeFileAtomic(filepath.Join(outputDir, "licenses", componentName, "THIRD_PARTY_COMPONENTS.json"), payload, 0o644)
}

func requireLicenseFiles(outputDir string) error {
	for _, name := range []string{"LICENSE-APACHE-2.0", "NOTICE"} {
		info, err := os.Stat(filepath.Join(outputDir, "licenses", componentName, name))
		if err != nil || !info.Mode().IsRegular() {
			if err == nil {
				err = errors.New("not a regular file")
			}
			return fmt.Errorf("staged sidecar license %s: %w", name, err)
		}
	}
	return nil
}

func sanitizedPackageSource(raw string) string {
	prefix, address, ok := strings.Cut(strings.TrimSpace(raw), "+")
	if !ok {
		return strings.TrimSpace(raw)
	}
	return prefix + "+" + sanitizedArtifactSource(address)
}

func updateManifest(outputDir, targetOS, targetArch string, component builtins.Component) error {
	manifestPath := filepath.Join(outputDir, "builtins.manifest.json")
	payload, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read %s (run stage-builtins first): %w", manifestPath, err)
	}
	var manifest builtins.Manifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		return fmt.Errorf("decode %s: %w", manifestPath, err)
	}
	if manifest.Platform.OS != targetOS || manifest.Platform.Arch != targetArch {
		return fmt.Errorf("manifest platform %s/%s does not match target %s/%s", manifest.Platform.OS, manifest.Platform.Arch, targetOS, targetArch)
	}
	for index := range manifest.Components {
		if manifest.Components[index].Name != componentName {
			continue
		}
		manifest.Components[index].SDKVersion = component.SDKVersion
		manifest.Components[index].License = component.License
		manifest.Components[index].Distribution = "checksum-verified-artifact"
		return writeManifest(manifestPath, manifest)
	}
	return errors.New("builtins manifest does not contain kbase-lance-engine")
}

func writeManifest(path string, manifest builtins.Manifest) error {
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return writeFileAtomic(path, payload, 0o644)
}

func writeFileAtomic(path string, payload []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".stage-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(payload); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return os.Rename(tempPath, path)
}

func sanitizedArtifactSource(raw string) string {
	// Cargo sources are URLs in normal releases. Do not persist credentials,
	// query strings, or fragments in a service bundle.
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" {
		return strings.TrimSpace(raw)
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}
