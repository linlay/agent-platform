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
	"path"
	"path/filepath"
	"sort"
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
	SDKVersion       string            `json:"sdkVersion,omitempty"`
	License          string            `json:"license,omitempty"`
	LicenseDirectory string            `json:"licenseDirectory,omitempty"`
	Licenses         []string          `json:"licenses,omitempty"`
	Targets          map[string]Target `json:"targets"`
}

type Target struct {
	Path     string          `json:"path"`
	Format   string          `json:"format,omitempty"`
	Entry    string          `json:"entry,omitempty"`
	Output   string          `json:"output,omitempty"`
	SHA256   string          `json:"sha256"`
	Tree     *TreeLayout     `json:"tree,omitempty"`
	Metadata *TargetMetadata `json:"metadata,omitempty"`
}

// TreeLayout describes a checksum-verified archive subtree that is installed
// as one builtin component. It is used for native helpers that cannot run as
// a single executable file because they carry dynamic libraries or data.
type TreeLayout struct {
	Root    string       `json:"root"`
	Outputs []TreeOutput `json:"outputs"`
}

// TreeOutput is a destination owned by an archive-tree component. Keeping the
// explicit list makes stale-file removal and archive validation deterministic.
type TreeOutput struct {
	Path string `json:"path"`
	Type string `json:"type"`
}

// TargetMetadata describes extra files that a platform archive carries beside
// its executable. It is currently used by the Lance sidecar release so the
// service package can preserve its dependency inventory and SBOM.
type TargetMetadata struct {
	CargoMetadata string `json:"cargoMetadata,omitempty"`
	SBOM          string `json:"sbom,omitempty"`
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
	Name         string       `json:"name"`
	Version      string       `json:"version"`
	Source       string       `json:"source,omitempty"`
	Commit       string       `json:"commit,omitempty"`
	Path         string       `json:"path"`
	SHA256       string       `json:"sha256"`
	SDKVersion   string       `json:"sdkVersion,omitempty"`
	License      string       `json:"license,omitempty"`
	Distribution string       `json:"distribution,omitempty"`
	Tree         []TreeOutput `json:"tree,omitempty"`
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
		target, ok := component.Targets[targetKey]
		if !ok {
			if component.Required {
				return StageResult{}, fmt.Errorf("required builtin %s has no target %s", component.Name, targetKey)
			}
			continue
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
		var staged ManifestComponent
		if component.Kind == "archive-tree" {
			digest, err := stageArchiveTreeAtomic(sourcePath, target.Format, *target.Tree, outputDir)
			if err != nil {
				return StageResult{}, fmt.Errorf("%s tree payload: %w", component.Name, err)
			}
			staged = ManifestComponent{
				Name:         component.Name,
				Version:      component.Version,
				Source:       component.Source,
				Commit:       component.Commit,
				Path:         target.Tree.Outputs[0].Path,
				SHA256:       digest,
				SDKVersion:   component.SDKVersion,
				License:      component.License,
				Distribution: "checksum-verified-artifact",
				Tree:         append([]TreeOutput(nil), target.Tree.Outputs...),
			}
		} else {
			if err := removeStaleOutputs(outputDir, component); err != nil {
				return StageResult{}, err
			}
			payload, err := ReadTargetPayload(sourcePath, component.Kind, target)
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
			staged = ManifestComponent{
				Name:       component.Name,
				Version:    component.Version,
				Source:     component.Source,
				Commit:     component.Commit,
				Path:       filepath.ToSlash(filepath.Join("bin", target.Output)),
				SHA256:     outputHash,
				SDKVersion: component.SDKVersion,
				License:    component.License,
			}
			if target.Metadata != nil {
				staged.Distribution = "checksum-verified-artifact"
			}
		}
		manifest.Components = append(manifest.Components, staged)
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
	if component.Kind != "file" && component.Kind != "archive" && component.Kind != "archive-tree" {
		return fmt.Errorf("builtin %s has unsupported kind %q", component.Name, component.Kind)
	}
	if len(component.Targets) == 0 {
		return fmt.Errorf("builtin %s targets are required", component.Name)
	}
	for targetKey, target := range component.Targets {
		if strings.TrimSpace(target.Path) == "" {
			return fmt.Errorf("builtin %s target %s path is required", component.Name, targetKey)
		}
		if component.Kind == "archive-tree" {
			if target.Tree == nil {
				return fmt.Errorf("builtin %s target %s archive tree is required", component.Name, targetKey)
			}
			if strings.TrimSpace(target.Output) != "" || strings.TrimSpace(target.Entry) != "" {
				return fmt.Errorf("builtin %s target %s archive tree cannot use entry or output", component.Name, targetKey)
			}
			if target.Format != "tar.gz" && target.Format != "zip" {
				return fmt.Errorf("builtin %s target %s archive tree format is invalid", component.Name, targetKey)
			}
			if err := validateTreeLayout(*target.Tree); err != nil {
				return fmt.Errorf("builtin %s target %s archive tree: %w", component.Name, targetKey, err)
			}
		} else {
			if strings.TrimSpace(target.Output) == "" {
				return fmt.Errorf("builtin %s target %s output is required", component.Name, targetKey)
			}
			if filepath.Base(target.Output) != target.Output {
				return fmt.Errorf("builtin %s target %s output must be a filename", component.Name, targetKey)
			}
			if target.Tree != nil {
				return fmt.Errorf("builtin %s target %s tree is only valid for archive-tree", component.Name, targetKey)
			}
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
		if target.Metadata != nil {
			if component.Kind != "archive" {
				return fmt.Errorf("builtin %s target %s metadata requires an archive", component.Name, targetKey)
			}
			if err := validateArchiveEntry(target.Metadata.CargoMetadata); err != nil {
				return fmt.Errorf("builtin %s target %s cargo metadata: %w", component.Name, targetKey, err)
			}
			if err := validateArchiveEntry(target.Metadata.SBOM); err != nil {
				return fmt.Errorf("builtin %s target %s SBOM: %w", component.Name, targetKey, err)
			}
		}
	}
	return nil
}

func validateTreeLayout(layout TreeLayout) error {
	if err := validateArchiveEntry(layout.Root); err != nil {
		return fmt.Errorf("root: %w", err)
	}
	if len(layout.Outputs) == 0 {
		return errors.New("outputs are required")
	}
	seen := map[string]bool{}
	for _, output := range layout.Outputs {
		clean, err := cleanRelativePath(output.Path)
		if err != nil {
			return fmt.Errorf("output %q: %w", output.Path, err)
		}
		if seen[clean] {
			return fmt.Errorf("duplicate output %q", clean)
		}
		seen[clean] = true
		if output.Type != "file" && output.Type != "dir" {
			return fmt.Errorf("output %q has invalid type %q", clean, output.Type)
		}
		for _, other := range layout.Outputs {
			otherClean, otherErr := cleanRelativePath(other.Path)
			if otherErr != nil || otherClean == clean {
				continue
			}
			if strings.HasPrefix(otherClean, clean+"/") || strings.HasPrefix(clean, otherClean+"/") {
				return fmt.Errorf("output %q overlaps output %q", clean, otherClean)
			}
		}
	}
	return nil
}

func validateArchiveEntry(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("entry is required")
	}
	clean := filepath.Clean(value)
	if filepath.IsAbs(value) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return errors.New("entry must stay inside archive")
	}
	return nil
}

// ReadTargetPayload reads the executable referenced by a verified target. The
// caller is responsible for verifying the artifact checksum before calling it.
func ReadTargetPayload(path string, kind string, target Target) ([]byte, error) {
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

// ReadArchiveEntry returns one regular-file entry from a locked archive.
func ReadArchiveEntry(path, format, entry string) ([]byte, error) {
	switch format {
	case "tar.gz":
		return readTarGzipEntry(path, entry)
	case "zip":
		return readZipEntry(path, entry)
	default:
		return nil, fmt.Errorf("unsupported archive format %q", format)
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

func cleanRelativePath(value string) (string, error) {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	if value == "" || strings.HasPrefix(value, "/") {
		return "", errors.New("path must be relative")
	}
	clean := path.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("path must stay inside root")
	}
	return clean, nil
}

func stageArchiveTreeAtomic(archivePath, format string, layout TreeLayout, outputDir string) (string, error) {
	if err := validateTreeLayout(layout); err != nil {
		return "", err
	}
	stageDir, err := os.MkdirTemp(outputDir, ".builtin-tree-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stageDir)
	stageRoot := filepath.Join(stageDir, "tree")
	if err := os.MkdirAll(stageRoot, 0o755); err != nil {
		return "", err
	}
	if err := extractArchiveTree(archivePath, format, layout, stageRoot); err != nil {
		return "", err
	}
	digest, err := TreeDigest(stageRoot, layout.Outputs)
	if err != nil {
		return "", err
	}
	if err := installTreeOutputs(stageDir, stageRoot, outputDir, layout.Outputs); err != nil {
		return "", err
	}
	return digest, nil
}

// TreeDigest produces a stable digest for all files and directories that a
// tree component owns. It is safe to persist in a program bundle manifest.
func TreeDigest(root string, outputs []TreeOutput) (string, error) {
	if err := validateTreeLayout(TreeLayout{Root: "tree", Outputs: outputs}); err != nil {
		return "", err
	}
	records := make([]string, 0)
	hashes := map[string]string{}
	for _, output := range outputs {
		clean, _ := cleanRelativePath(output.Path)
		item, err := joinWithin(root, clean)
		if err != nil {
			return "", err
		}
		info, err := os.Lstat(item)
		if err != nil {
			return "", fmt.Errorf("tree output %s: %w", clean, err)
		}
		if output.Type == "file" && !info.Mode().IsRegular() {
			return "", fmt.Errorf("tree output %s is not a regular file", clean)
		}
		if output.Type == "dir" && !info.IsDir() {
			return "", fmt.Errorf("tree output %s is not a directory", clean)
		}
		walkErr := filepath.WalkDir(item, func(current string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
				return fmt.Errorf("tree output %s contains unsupported entry %s", clean, current)
			}
			relative, err := filepath.Rel(root, current)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			if info.IsDir() {
				records = append(records, "d\x00"+relative+"\x00"+fmt.Sprintf("%04o", info.Mode().Perm()))
				return nil
			}
			file, err := os.Open(current)
			if err != nil {
				return err
			}
			hash := sha256.New()
			_, copyErr := io.Copy(hash, file)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			hashes[relative] = hex.EncodeToString(hash.Sum(nil))
			records = append(records, "f\x00"+relative+"\x00"+fmt.Sprintf("%04o", info.Mode().Perm()))
			return nil
		})
		if walkErr != nil {
			return "", walkErr
		}
	}
	sort.Strings(records)
	hash := sha256.New()
	for _, record := range records {
		_, _ = io.WriteString(hash, record)
		_, _ = io.WriteString(hash, "\x00")
		if strings.HasPrefix(record, "f\x00") {
			parts := strings.Split(record, "\x00")
			_, _ = io.WriteString(hash, hashes[parts[1]])
			_, _ = io.WriteString(hash, "\x00")
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func extractArchiveTree(archivePath, format string, layout TreeLayout, destination string) error {
	seen := map[string]bool{}
	writeEntry := func(name string, mode os.FileMode, directory bool, input io.Reader) error {
		archiveEntry, err := cleanRelativePath(name)
		if err != nil {
			return fmt.Errorf("unsafe archive tree entry %q: %w", name, err)
		}
		if seen[archiveEntry] {
			return fmt.Errorf("duplicate archive tree entry %q", archiveEntry)
		}
		seen[archiveEntry] = true
		relative, err := treeRelativePath(name, layout)
		if err != nil {
			return err
		}
		if relative == "" {
			if !directory {
				return fmt.Errorf("tree root %q must be a directory", layout.Root)
			}
			return nil
		}
		if !treePathAllowed(relative, directory, layout.Outputs) {
			return fmt.Errorf("archive tree entry %q is outside declared outputs", relative)
		}
		target, err := joinWithin(destination, relative)
		if err != nil {
			return err
		}
		if directory {
			return os.MkdirAll(target, 0o755)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, archiveMode(mode))
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(file, input)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}

	switch format {
	case "tar.gz":
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
				break
			}
			if err != nil {
				return err
			}
			switch header.Typeflag {
			case tar.TypeDir:
				if err := writeEntry(header.Name, os.FileMode(header.Mode), true, nil); err != nil {
					return err
				}
			case tar.TypeReg, tar.TypeRegA:
				if err := writeEntry(header.Name, os.FileMode(header.Mode), false, reader); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unsupported archive tree entry type for %q", header.Name)
			}
		}
	case "zip":
		reader, err := zip.OpenReader(archivePath)
		if err != nil {
			return err
		}
		defer reader.Close()
		for _, entry := range reader.File {
			if entry.FileInfo().IsDir() {
				if err := writeEntry(entry.Name, entry.Mode(), true, nil); err != nil {
					return err
				}
				continue
			}
			if !entry.Mode().IsRegular() {
				return fmt.Errorf("unsupported archive tree entry type for %q", entry.Name)
			}
			input, err := entry.Open()
			if err != nil {
				return err
			}
			err = writeEntry(entry.Name, entry.Mode(), false, input)
			closeErr := input.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
		}
	default:
		return fmt.Errorf("unsupported archive format %q", format)
	}
	for _, output := range layout.Outputs {
		clean, _ := cleanRelativePath(output.Path)
		item, err := joinWithin(destination, clean)
		if err != nil {
			return err
		}
		info, err := os.Lstat(item)
		if err != nil {
			return fmt.Errorf("archive tree output %s: %w", clean, err)
		}
		if (output.Type == "file" && !info.Mode().IsRegular()) || (output.Type == "dir" && !info.IsDir()) {
			return fmt.Errorf("archive tree output %s type mismatch", clean)
		}
	}
	return nil
}

func treeRelativePath(name string, layout TreeLayout) (string, error) {
	clean, err := cleanRelativePath(name)
	if err != nil {
		return "", fmt.Errorf("unsafe archive tree entry %q: %w", name, err)
	}
	root, _ := cleanRelativePath(layout.Root)
	if clean == root {
		return "", nil
	}
	prefix := root + "/"
	if !strings.HasPrefix(clean, prefix) {
		return "", fmt.Errorf("archive tree entry %q is outside root %q", name, layout.Root)
	}
	return strings.TrimPrefix(clean, prefix), nil
}

func treePathAllowed(value string, directory bool, outputs []TreeOutput) bool {
	for _, output := range outputs {
		clean, _ := cleanRelativePath(output.Path)
		if clean == value {
			return output.Type == "dir" || !directory
		}
		if output.Type == "dir" && strings.HasPrefix(value, clean+"/") {
			return true
		}
		if directory && strings.HasPrefix(clean, value+"/") {
			return true
		}
	}
	return false
}

func archiveMode(mode os.FileMode) os.FileMode {
	if mode.Perm()&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

type treeReplacement struct {
	staged      string
	destination string
	backup      string
}

func installTreeOutputs(stageDir, stageRoot, outputDir string, outputs []TreeOutput) error {
	replacements := make([]treeReplacement, 0, len(outputs))
	backupRoot := filepath.Join(stageDir, "backup")
	for _, output := range outputs {
		clean, _ := cleanRelativePath(output.Path)
		staged, err := joinWithin(stageRoot, clean)
		if err != nil {
			return err
		}
		destination, err := joinWithin(outputDir, clean)
		if err != nil {
			return err
		}
		backup, err := joinWithin(backupRoot, clean)
		if err != nil {
			return err
		}
		replacements = append(replacements, treeReplacement{staged: staged, destination: destination, backup: backup})
	}
	for _, replacement := range replacements {
		if _, err := os.Lstat(replacement.destination); err == nil {
			if err := os.MkdirAll(filepath.Dir(replacement.backup), 0o755); err != nil {
				rollbackTreeInstall(replacements, 0)
				return err
			}
			if err := os.Rename(replacement.destination, replacement.backup); err != nil {
				rollbackTreeInstall(replacements, 0)
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			rollbackTreeInstall(replacements, 0)
			return err
		}
	}
	installed := 0
	for _, replacement := range replacements {
		if err := os.MkdirAll(filepath.Dir(replacement.destination), 0o755); err != nil {
			rollbackTreeInstall(replacements, installed)
			return err
		}
		if err := os.Rename(replacement.staged, replacement.destination); err != nil {
			rollbackTreeInstall(replacements, installed)
			return err
		}
		installed++
	}
	return nil
}

func rollbackTreeInstall(replacements []treeReplacement, installed int) {
	for index := installed - 1; index >= 0; index-- {
		_ = os.RemoveAll(replacements[index].destination)
		if _, err := os.Lstat(replacements[index].backup); err == nil {
			_ = os.Rename(replacements[index].backup, replacements[index].destination)
		}
	}
	for index := installed; index < len(replacements); index++ {
		if _, err := os.Lstat(replacements[index].backup); err == nil {
			_ = os.Rename(replacements[index].backup, replacements[index].destination)
		}
	}
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
