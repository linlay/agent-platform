package builtins

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const lockSchemaVersion = 1

type Lock struct {
	SchemaVersion int         `json:"schemaVersion"`
	DefaultRoot   string      `json:"defaultRoot"`
	Components    []Component `json:"components"`
}

type Component struct {
	Name             string            `json:"name"`
	Version          string            `json:"version"`
	Repository       string            `json:"repository"`
	Source           string            `json:"source,omitempty"`
	Commit           string            `json:"commit,omitempty"`
	Kind             string            `json:"kind"`
	Required         bool              `json:"required"`
	Distribution     string            `json:"distribution,omitempty"`
	BuildPath        string            `json:"buildPath,omitempty"`
	BuildTargets     map[string]string `json:"buildTargets,omitempty"`
	SDKVersion       string            `json:"sdkVersion,omitempty"`
	License          string            `json:"license,omitempty"`
	LicenseDirectory string            `json:"licenseDirectory,omitempty"`
	Licenses         []string          `json:"licenses,omitempty"`
	Targets          map[string]Target `json:"targets"`
}

type Target struct {
	Path   string `json:"path"`
	Format string `json:"format,omitempty"`
	Entry  string `json:"entry,omitempty"`
	Output string `json:"output"`
	SHA256 string `json:"sha256"`
}

type StageOptions struct {
	RepoRoot     string
	LockPath     string
	BuiltinsRoot string
	OutputDir    string
	GOOS         string
	GOARCH       string
}

type Manifest struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Platform      ManifestPlatform    `json:"platform"`
	Components    []ManifestComponent `json:"components"`
}

type ManifestPlatform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

type ManifestComponent struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	Source       string `json:"source,omitempty"`
	Commit       string `json:"commit,omitempty"`
	Path         string `json:"path"`
	SHA256       string `json:"sha256"`
	SDKVersion   string `json:"sdkVersion,omitempty"`
	License      string `json:"license,omitempty"`
	Distribution string `json:"distribution,omitempty"`
}

type StageResult struct {
	BuiltinsRoot string
	ManifestPath string
	Manifest     Manifest
}

func LoadLock(path string) (Lock, error) {
	file, err := os.Open(path)
	if err != nil {
		return Lock{}, err
	}
	defer file.Close()

	var lock Lock
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&lock); err != nil {
		return Lock{}, fmt.Errorf("decode builtins lock: %w", err)
	}
	if lock.SchemaVersion != lockSchemaVersion {
		return Lock{}, fmt.Errorf("unsupported builtins lock schema %d", lock.SchemaVersion)
	}
	if strings.TrimSpace(lock.DefaultRoot) == "" {
		return Lock{}, errors.New("builtins lock defaultRoot is required")
	}
	if len(lock.Components) == 0 {
		return Lock{}, errors.New("builtins lock components are required")
	}
	seen := map[string]bool{}
	for _, component := range lock.Components {
		if err := validateComponent(component); err != nil {
			return Lock{}, err
		}
		if seen[component.Name] {
			return Lock{}, fmt.Errorf("duplicate builtin component %q", component.Name)
		}
		seen[component.Name] = true
	}
	return lock, nil
}

func ResolveRoot(repoRoot string, override string, lock Lock) (string, error) {
	repoRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", err
	}
	override = strings.TrimSpace(override)
	if override == "" {
		override = strings.TrimSpace(os.Getenv("BUILTINS_ROOT"))
	}
	if override != "" {
		if !filepath.IsAbs(override) {
			return "", errors.New("BUILTINS_ROOT must be an absolute path")
		}
		return filepath.Clean(override), nil
	}
	return filepath.Clean(filepath.Join(repoRoot, lock.DefaultRoot)), nil
}

func FindComponent(lock Lock, name string) (Component, error) {
	for _, component := range lock.Components {
		if component.Name == name {
			return component, nil
		}
	}
	return Component{}, fmt.Errorf("builtin component %q is not locked", name)
}

func Stage(options StageOptions) (StageResult, error) {
	repoRoot, err := filepath.Abs(options.RepoRoot)
	if err != nil {
		return StageResult{}, err
	}
	lockPath := options.LockPath
	if !filepath.IsAbs(lockPath) {
		lockPath = filepath.Join(repoRoot, lockPath)
	}
	lock, err := LoadLock(lockPath)
	if err != nil {
		return StageResult{}, err
	}
	builtinsRoot, err := ResolveRoot(repoRoot, options.BuiltinsRoot, lock)
	if err != nil {
		return StageResult{}, err
	}
	outputDir := options.OutputDir
	if !filepath.IsAbs(outputDir) {
		outputDir = filepath.Join(repoRoot, outputDir)
	}
	targetKey := strings.TrimSpace(options.GOOS) + "-" + strings.TrimSpace(options.GOARCH)
	if targetKey == "-" {
		return StageResult{}, errors.New("target os and arch are required")
	}
	if err := os.MkdirAll(filepath.Join(outputDir, "bin"), 0o755); err != nil {
		return StageResult{}, err
	}

	manifest := Manifest{
		SchemaVersion: lockSchemaVersion,
		Platform: ManifestPlatform{
			OS:   options.GOOS,
			Arch: options.GOARCH,
		},
	}
	for _, component := range lock.Components {
		// source-build components are staged by their dedicated, toolchain-aware
		// release step. The generic builtin stager still validates and registers
		// them, but never pretends that an unsigned/unbuilt artifact exists.
		if component.Distribution == "source-build" {
			continue
		}
		target, ok := component.Targets[targetKey]
		if !ok {
			if component.Required {
				return StageResult{}, fmt.Errorf("required builtin %s has no target %s", component.Name, targetKey)
			}
			continue
		}
		if err := removeStaleOutputs(outputDir, component); err != nil {
			return StageResult{}, err
		}
		repositoryRoot, err := joinWithin(builtinsRoot, component.Repository)
		if err != nil {
			return StageResult{}, err
		}
		sourcePath, err := joinWithin(repositoryRoot, target.Path)
		if err != nil {
			return StageResult{}, err
		}
		if err := verifyFileSHA256(sourcePath, target.SHA256); err != nil {
			return StageResult{}, fmt.Errorf("%s source verification: %w", component.Name, err)
		}
		payload, err := readPayload(sourcePath, component.Kind, target)
		if err != nil {
			return StageResult{}, fmt.Errorf("%s payload: %w", component.Name, err)
		}
		destination, err := joinWithin(filepath.Join(outputDir, "bin"), target.Output)
		if err != nil {
			return StageResult{}, err
		}
		if err := writeFileAtomic(destination, payload, 0o755); err != nil {
			return StageResult{}, err
		}
		outputHash := bytesSHA256(payload)
		manifest.Components = append(manifest.Components, ManifestComponent{
			Name:    component.Name,
			Version: component.Version,
			Source:  component.Source,
			Commit:  component.Commit,
			Path:    filepath.ToSlash(filepath.Join("bin", target.Output)),
			SHA256:  outputHash,
		})
		if err := stageLicenses(repositoryRoot, outputDir, component); err != nil {
			return StageResult{}, err
		}
	}

	manifestPath := filepath.Join(outputDir, "builtins.manifest.json")
	if err := writeJSONAtomic(manifestPath, manifest); err != nil {
		return StageResult{}, err
	}
	return StageResult{
		BuiltinsRoot: builtinsRoot,
		ManifestPath: manifestPath,
		Manifest:     manifest,
	}, nil
}

func validateComponent(component Component) error {
	if strings.TrimSpace(component.Name) == "" ||
		strings.TrimSpace(component.Version) == "" ||
		strings.TrimSpace(component.Repository) == "" {
		return errors.New("builtin component name, version, and repository are required")
	}
	if component.Kind != "file" && component.Kind != "archive" {
		return fmt.Errorf("builtin %s has unsupported kind %q", component.Name, component.Kind)
	}
	if component.Distribution != "" && component.Distribution != "source-build" {
		return fmt.Errorf("builtin %s has unsupported distribution %q", component.Name, component.Distribution)
	}
	if component.Distribution == "source-build" {
		buildPath := strings.TrimSpace(component.BuildPath)
		if buildPath == "" {
			return fmt.Errorf("builtin %s source-build buildPath is required", component.Name)
		}
		cleanBuildPath := filepath.Clean(buildPath)
		if filepath.IsAbs(buildPath) || cleanBuildPath == ".." || strings.HasPrefix(cleanBuildPath, ".."+string(filepath.Separator)) {
			return fmt.Errorf("builtin %s source-build buildPath must stay within the repository", component.Name)
		}
		if strings.TrimSpace(component.SDKVersion) == "" {
			return fmt.Errorf("builtin %s source-build sdkVersion is required", component.Name)
		}
		if strings.TrimSpace(component.License) == "" {
			return fmt.Errorf("builtin %s source-build license is required", component.Name)
		}
		if len(component.BuildTargets) == 0 {
			return fmt.Errorf("builtin %s source-build buildTargets are required", component.Name)
		}
		for targetKey, output := range component.BuildTargets {
			if strings.TrimSpace(targetKey) == "" || filepath.Base(output) != output || strings.TrimSpace(output) == "" {
				return fmt.Errorf("builtin %s source-build target %s has invalid output %q", component.Name, targetKey, output)
			}
			if !validPlatformTargetKey(targetKey) {
				return fmt.Errorf("builtin %s source-build target %s is unsupported", component.Name, targetKey)
			}
		}
		return nil
	}
	if len(component.Targets) == 0 {
		return fmt.Errorf("builtin %s targets are required", component.Name)
	}
	for targetKey, target := range component.Targets {
		if strings.TrimSpace(target.Path) == "" || strings.TrimSpace(target.Output) == "" {
			return fmt.Errorf("builtin %s target %s path and output are required", component.Name, targetKey)
		}
		if filepath.Base(target.Output) != target.Output {
			return fmt.Errorf("builtin %s target %s output must be a filename", component.Name, targetKey)
		}
		if len(target.SHA256) != sha256.Size*2 {
			return fmt.Errorf("builtin %s target %s has invalid sha256", component.Name, targetKey)
		}
		if _, err := hex.DecodeString(target.SHA256); err != nil {
			return fmt.Errorf("builtin %s target %s has invalid sha256", component.Name, targetKey)
		}
		if component.Kind == "archive" && (target.Entry == "" || (target.Format != "tar.gz" && target.Format != "zip")) {
			return fmt.Errorf("builtin %s target %s archive entry and format are required", component.Name, targetKey)
		}
	}
	return nil
}

func validPlatformTargetKey(value string) bool {
	parts := strings.Split(value, "-")
	if len(parts) != 2 {
		return false
	}
	validOS := parts[0] == "darwin" || parts[0] == "linux" || parts[0] == "windows"
	validArch := parts[1] == "amd64" || parts[1] == "arm64"
	return validOS && validArch
}

func readPayload(path string, kind string, target Target) ([]byte, error) {
	if kind == "file" {
		return os.ReadFile(path)
	}
	switch target.Format {
	case "tar.gz":
		return readTarGzipEntry(path, target.Entry)
	case "zip":
		return readZipEntry(path, target.Entry)
	default:
		return nil, fmt.Errorf("unsupported archive format %q", target.Format)
	}
}

func readTarGzipEntry(path string, entry string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	expected := normalizeArchiveEntry(entry)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if normalizeArchiveEntry(header.Name) != expected {
			continue
		}
		if !header.FileInfo().Mode().IsRegular() {
			return nil, fmt.Errorf("archive entry %s is not a regular file", entry)
		}
		return io.ReadAll(reader)
	}
	return nil, fmt.Errorf("archive entry %s not found", entry)
}

func readZipEntry(path string, entry string) ([]byte, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	expected := normalizeArchiveEntry(entry)
	for _, file := range reader.File {
		if normalizeArchiveEntry(file.Name) != expected {
			continue
		}
		if !file.Mode().IsRegular() {
			return nil, fmt.Errorf("archive entry %s is not a regular file", entry)
		}
		content, err := file.Open()
		if err != nil {
			return nil, err
		}
		payload, readErr := io.ReadAll(content)
		closeErr := content.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		return payload, nil
	}
	return nil, fmt.Errorf("archive entry %s not found", entry)
}

func normalizeArchiveEntry(value string) string {
	return strings.TrimPrefix(filepath.ToSlash(filepath.Clean(value)), "./")
}

func stageLicenses(repositoryRoot string, outputDir string, component Component) error {
	if len(component.Licenses) == 0 {
		return nil
	}
	directory := component.LicenseDirectory
	if directory == "" {
		directory = component.Name
	}
	destinationRoot, err := joinWithin(filepath.Join(outputDir, "licenses"), directory)
	if err != nil {
		return err
	}
	for _, license := range component.Licenses {
		source, err := joinWithin(repositoryRoot, license)
		if err != nil {
			return err
		}
		payload, err := os.ReadFile(source)
		if err != nil {
			return fmt.Errorf("%s license %s: %w", component.Name, license, err)
		}
		destination, err := joinWithin(destinationRoot, filepath.Base(license))
		if err != nil {
			return err
		}
		if err := writeFileAtomic(destination, payload, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func removeStaleOutputs(outputDir string, component Component) error {
	seen := map[string]bool{}
	for _, target := range component.Targets {
		if seen[target.Output] {
			continue
		}
		seen[target.Output] = true
		path, err := joinWithin(filepath.Join(outputDir, "bin"), target.Output)
		if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func verifyFileSHA256(path string, expected string) error {
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
	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("sha256 mismatch for %s: expected %s, got %s", path, expected, actual)
	}
	return nil
}

func bytesSHA256(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func writeFileAtomic(path string, payload []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(payload); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tempPath, mode); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(tempPath, path)
}

func writeJSONAtomic(path string, value any) error {
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return writeFileAtomic(path, payload, 0o644)
}

func joinWithin(root string, path string) (string, error) {
	root = filepath.Clean(root)
	candidate := filepath.Clean(filepath.Join(root, path))
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return "", err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes root: %s", path)
	}
	return candidate, nil
}
