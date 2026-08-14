package runtimeresource

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const (
	archiveRoot            = "env"
	providerRegisterFile   = "provider-register.json"
	maxArchiveFileBytes    = int64(2 << 30)
	maxArchiveContentBytes = int64(8 << 30)
)

var resourceScopes = []string{"agents", "skills-center", "tools", "teams", "registries"}

var unitScopes = []string{"agents", "skills-center", "tools", "teams"}

type archiveInventory struct {
	version              string
	versionRaw           string
	sha256               string
	extractedRoot        string
	providerRegisterPath string
	units                map[string]struct{}
	registryFiles        map[string]struct{}
}

func extractArchive(sourcePath, expectedVersion, workRoot string) (archiveInventory, error) {
	resolved, err := filepath.Abs(strings.TrimSpace(sourcePath))
	if err != nil {
		return archiveInventory{}, fmt.Errorf("resolve runtime resource source: %w", err)
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return archiveInventory{}, fmt.Errorf("inspect runtime resource source: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return archiveInventory{}, fmt.Errorf("runtime resource source must be a regular non-symlink file: %s", resolved)
	}

	digest, err := fileSHA256(resolved)
	if err != nil {
		return archiveInventory{}, fmt.Errorf("hash runtime resource source: %w", err)
	}
	reader, err := zip.OpenReader(resolved)
	if err != nil {
		return archiveInventory{}, fmt.Errorf("open runtime resource ZIP: %w", err)
	}
	defer reader.Close()

	extractedRoot := filepath.Join(workRoot, "source")
	if err := os.MkdirAll(extractedRoot, 0o700); err != nil {
		return archiveInventory{}, fmt.Errorf("create runtime resource staging: %w", err)
	}
	seen := map[string]bool{}
	var totalBytes int64
	for _, entry := range reader.File {
		relative, directory, err := validateArchiveEntry(entry)
		if err != nil {
			return archiveInventory{}, err
		}
		if relative == "" {
			continue
		}
		key := archivePathKey(relative)
		if _, exists := seen[key]; exists {
			return archiveInventory{}, fmt.Errorf("duplicate runtime resource ZIP path: %s", relative)
		}
		seen[key] = directory
		parent := path.Dir(relative)
		for parent != "." && parent != "" {
			if parentDirectory, exists := seen[archivePathKey(parent)]; exists && !parentDirectory {
				return archiveInventory{}, fmt.Errorf("runtime resource ZIP file/directory conflict: %s", relative)
			}
			parent = path.Dir(parent)
		}
		if !directory && entry.UncompressedSize64 > uint64(maxArchiveFileBytes) {
			return archiveInventory{}, fmt.Errorf("runtime resource ZIP entry is too large: %s", relative)
		}
		totalBytes += int64(entry.UncompressedSize64)
		if totalBytes > maxArchiveContentBytes {
			return archiveInventory{}, fmt.Errorf("runtime resource ZIP expands beyond the safety limit")
		}
		if !isPlatformResourcePath(relative) {
			continue
		}
		target, err := safeJoin(extractedRoot, filepath.FromSlash(relative))
		if err != nil {
			return archiveInventory{}, err
		}
		if directory {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return archiveInventory{}, fmt.Errorf("create staged directory %s: %w", relative, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return archiveInventory{}, fmt.Errorf("create staged parent for %s: %w", relative, err)
		}
		if err := extractZipFile(entry, target); err != nil {
			return archiveInventory{}, fmt.Errorf("extract runtime resource %s: %w", relative, err)
		}
		if runtime.GOOS != "windows" && strings.EqualFold(filepath.Ext(target), ".sh") {
			if err := os.Chmod(target, 0o755); err != nil {
				return archiveInventory{}, fmt.Errorf("make staged shell script executable %s: %w", relative, err)
			}
		}
	}

	versionBytes, err := os.ReadFile(filepath.Join(extractedRoot, "VERSION"))
	if err != nil {
		return archiveInventory{}, fmt.Errorf("runtime resource ZIP requires env/VERSION: %w", err)
	}
	versionRaw := strings.TrimSpace(string(versionBytes))
	version := normalizeVersion(versionRaw)
	if version == "" {
		return archiveInventory{}, fmt.Errorf("runtime resource ZIP env/VERSION is empty")
	}
	if expected := normalizeVersion(expectedVersion); expected != "" && version != expected {
		return archiveInventory{}, fmt.Errorf("runtime resource VERSION mismatch: package=%s desktop=%s", version, expected)
	}
	units, registryFiles, err := inventoryExtractedTree(extractedRoot)
	if err != nil {
		return archiveInventory{}, err
	}
	providerRegisterPath, err := optionalRegularFilePath(filepath.Join(extractedRoot, providerRegisterFile))
	if err != nil {
		return archiveInventory{}, err
	}
	return archiveInventory{
		version:              version,
		versionRaw:           versionRaw,
		sha256:               digest,
		extractedRoot:        extractedRoot,
		providerRegisterPath: providerRegisterPath,
		units:                units,
		registryFiles:        registryFiles,
	}, nil
}

func validateArchiveEntry(entry *zip.File) (string, bool, error) {
	name := entry.Name
	if strings.ContainsRune(name, '\\') || strings.ContainsRune(name, '\x00') {
		return "", false, fmt.Errorf("unsafe runtime resource ZIP path: %q", name)
	}
	cleaned := path.Clean(name)
	if cleaned == "." || strings.HasPrefix(cleaned, "/") || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false, fmt.Errorf("unsafe runtime resource ZIP path: %q", name)
	}
	parts := strings.Split(cleaned, "/")
	if len(parts) == 0 || parts[0] != archiveRoot {
		return "", false, fmt.Errorf("runtime resource ZIP must have a single env/ root: %q", name)
	}
	mode := entry.Mode()
	if mode&os.ModeSymlink != 0 || mode&(os.ModeDevice|os.ModeNamedPipe|os.ModeSocket) != 0 {
		return "", false, fmt.Errorf("runtime resource ZIP contains unsupported file type: %q", name)
	}
	directory := entry.FileInfo().IsDir() || strings.HasSuffix(name, "/")
	if cleaned == archiveRoot {
		if !directory {
			return "", false, fmt.Errorf("runtime resource ZIP env root must be a directory")
		}
		return "", true, nil
	}
	return strings.TrimPrefix(cleaned, archiveRoot+"/"), directory, nil
}

func isPlatformResourcePath(relative string) bool {
	if relative == "VERSION" || relative == providerRegisterFile {
		return true
	}
	first := strings.SplitN(relative, "/", 2)[0]
	for _, scope := range resourceScopes {
		if first == scope {
			return true
		}
	}
	return false
}

func optionalRegularFilePath(filePath string) (string, error) {
	info, err := os.Lstat(filePath)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("runtime resource ZIP %s must be a regular file", providerRegisterFile)
	}
	return filePath, nil
}

func extractZipFile(entry *zip.File, target string) error {
	source, err := entry.Open()
	if err != nil {
		return err
	}
	defer source.Close()
	temporary := target + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(file, io.LimitReader(source, maxArchiveFileBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(temporary)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(temporary)
		return closeErr
	}
	if written > maxArchiveFileBytes || uint64(written) != entry.UncompressedSize64 {
		_ = os.Remove(temporary)
		return fmt.Errorf("expanded size does not match ZIP metadata")
	}
	if err := os.Rename(temporary, target); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func inventoryExtractedTree(root string) (map[string]struct{}, map[string]struct{}, error) {
	units := map[string]struct{}{}
	for _, scope := range unitScopes {
		scopeRoot := filepath.Join(root, scope)
		entries, err := os.ReadDir(scopeRoot)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read staged %s resources: %w", scope, err)
		}
		for _, entry := range entries {
			if !safeUnitName(entry.Name()) {
				return nil, nil, fmt.Errorf("unsafe top-level %s resource name: %q", scope, entry.Name())
			}
			units[scope+"/"+entry.Name()] = struct{}{}
		}
	}

	registryFiles := map[string]struct{}{}
	registriesRoot := filepath.Join(root, "registries")
	err := filepath.WalkDir(registriesRoot, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if errors.Is(walkErr, os.ErrNotExist) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("staged Registry contains a symlink: %s", filePath)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("staged Registry contains an unsupported file: %s", filePath)
		}
		relative, err := filepath.Rel(root, filePath)
		if err != nil {
			return err
		}
		registryFiles[filepath.ToSlash(relative)] = struct{}{}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("inventory staged Registries: %w", err)
	}
	return units, registryFiles, nil
}

func fileSHA256(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func normalizeVersion(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "v")
}

func safeUnitName(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, "/\\\x00")
}

func archivePathKey(value string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(value)
	}
	return value
}

func sortedSet(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
