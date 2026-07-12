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
	repoRoot := flag.String("repo-root", ".", "repository root")
	lockPath := flag.String("lock", "scripts/release-assets/builtins.lock.json", "builtins lock path")
	outputDir := flag.String("output", "release-local", "bundle root")
	targetOS := flag.String("os", runtime.GOOS, "target operating system")
	targetArch := flag.String("arch", runtime.GOARCH, "target architecture")
	binaryPath := flag.String("binary", "", "sidecar binary to stage")
	expectedSHA := flag.String("expected-sha256", "", "required SHA-256 for verified artifacts")
	artifactSource := flag.String("artifact-source", "", "artifact URL or release source recorded in the manifest")
	cargoMetadata := flag.String("cargo-metadata", "", "Cargo metadata JSON used to generate a sanitized dependency inventory")
	localBuild := flag.Bool("local-build", false, "accept a local source build and record its computed SHA-256")
	flag.Parse()

	if err := run(*repoRoot, *lockPath, *outputDir, *targetOS, *targetArch, *binaryPath, *expectedSHA, *artifactSource, *cargoMetadata, *localBuild); err != nil {
		fmt.Fprintf(os.Stderr, "stage kbase-lance-engine: %v\n", err)
		os.Exit(1)
	}
}

func run(repoRoot, lockPath, outputDir, targetOS, targetArch, binaryPath, expectedSHA, artifactSource, cargoMetadataPath string, localBuild bool) error {
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
	if component.Distribution != "source-build" {
		return fmt.Errorf("component %s must use source-build distribution", componentName)
	}
	if strings.TrimSpace(binaryPath) == "" {
		return errors.New("--binary is required; build the target artifact first")
	}
	if !filepath.IsAbs(binaryPath) {
		binaryPath = filepath.Join(repoRoot, binaryPath)
	}
	if !filepath.IsAbs(outputDir) {
		outputDir = filepath.Join(repoRoot, outputDir)
	}

	actualSHA, err := fileSHA256(binaryPath)
	if err != nil {
		return fmt.Errorf("read target binary %s: %w", binaryPath, err)
	}
	expectedSHA = strings.ToLower(strings.TrimSpace(expectedSHA))
	if !localBuild {
		if len(expectedSHA) != sha256.Size*2 {
			return errors.New("--expected-sha256 is required for release artifacts (or use --local-build for a local source build)")
		}
		if _, err := hex.DecodeString(expectedSHA); err != nil {
			return fmt.Errorf("invalid expected SHA-256: %w", err)
		}
		if actualSHA != expectedSHA {
			return fmt.Errorf("sha256 mismatch for %s: expected %s, got %s", binaryPath, expectedSHA, actualSHA)
		}
	}

	binaryName := componentName
	if targetOS == "windows" {
		binaryName += ".exe"
	}
	lockedOutput, ok := component.BuildTargets[targetOS+"-"+targetArch]
	if !ok {
		return fmt.Errorf("component %s does not register target %s/%s", componentName, targetOS, targetArch)
	}
	if lockedOutput != binaryName {
		return fmt.Errorf("component %s target output is %q, want %q", componentName, lockedOutput, binaryName)
	}
	destination := filepath.Join(outputDir, "bin", binaryName)
	if err := copyExecutable(binaryPath, destination); err != nil {
		return err
	}
	if err := stageLicenseFiles(repoRoot, outputDir); err != nil {
		return err
	}
	if strings.TrimSpace(cargoMetadataPath) != "" {
		if !filepath.IsAbs(cargoMetadataPath) {
			cargoMetadataPath = filepath.Join(repoRoot, cargoMetadataPath)
		}
		if err := stageDependencyInventory(cargoMetadataPath, outputDir); err != nil {
			return err
		}
	}
	provenance := "checksum-verified-artifact"
	if localBuild {
		provenance = "local-source-build"
	}
	if err := updateManifest(outputDir, targetOS, targetArch, component, actualSHA, binaryName, artifactSource, provenance); err != nil {
		return err
	}
	fmt.Printf("staged %s %s for %s/%s (%s)\n", componentName, component.Version, targetOS, targetArch, actualSHA)
	return nil
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

func copyExecutable(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("source is not a regular file: %s", source)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(destination), ".kbase-lance-engine-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := io.Copy(temp, input); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Chmod(0o755); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return replaceFile(tempPath, destination)
}

func stageLicenseFiles(repoRoot, outputDir string) error {
	for _, name := range []string{"LICENSE-APACHE-2.0", "NOTICE"} {
		source := filepath.Join(repoRoot, "scripts", "release-assets", "licenses", componentName, name)
		payload, err := os.ReadFile(source)
		if err != nil {
			return fmt.Errorf("read sidecar license file %s: %w", name, err)
		}
		destination := filepath.Join(outputDir, "licenses", componentName, name)
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(destination, payload, 0o644); err != nil {
			return err
		}
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

func stageDependencyInventory(metadataPath, outputDir string) error {
	payload, err := os.ReadFile(metadataPath)
	if err != nil {
		return fmt.Errorf("read Cargo metadata: %w", err)
	}
	var metadata cargoMetadataDocument
	if err := json.Unmarshal(payload, &metadata); err != nil {
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
	payload, err = json.MarshalIndent(inventory, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	destination := filepath.Join(outputDir, "licenses", componentName, "THIRD_PARTY_COMPONENTS.json")
	return os.WriteFile(destination, payload, 0o644)
}

func sanitizedPackageSource(raw string) string {
	prefix, address, ok := strings.Cut(strings.TrimSpace(raw), "+")
	if !ok {
		return strings.TrimSpace(raw)
	}
	return prefix + "+" + sanitizedArtifactSource(address)
}

func updateManifest(outputDir, targetOS, targetArch string, component builtins.Component, actualSHA, binaryName, artifactSource, provenance string) error {
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
	components := manifest.Components[:0]
	for _, existing := range manifest.Components {
		if existing.Name != componentName {
			components = append(components, existing)
		}
	}
	source := component.Source
	if strings.TrimSpace(artifactSource) != "" {
		source = sanitizedArtifactSource(artifactSource)
	}
	manifest.Components = append(components, builtins.ManifestComponent{
		Name:         component.Name,
		Version:      component.Version,
		Source:       source,
		Path:         filepath.ToSlash(filepath.Join("bin", binaryName)),
		SHA256:       actualSHA,
		SDKVersion:   component.SDKVersion,
		License:      component.License,
		Distribution: provenance,
	})
	sort.SliceStable(manifest.Components, func(i, j int) bool {
		return manifest.Components[i].Name < manifest.Components[j].Name
	})
	payload, err = json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	temp, err := os.CreateTemp(outputDir, ".builtins.manifest-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(payload); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Chmod(0o644); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return replaceFile(tempPath, manifestPath)
}

func sanitizedArtifactSource(raw string) string {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" {
		return raw
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func replaceFile(source, destination string) error {
	if runtime.GOOS == "windows" {
		if err := os.Remove(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return os.Rename(source, destination)
}
