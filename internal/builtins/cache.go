package builtins

import (
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

// CacheStageOptions identifies a target cache prepared by
// sync-local-builtins. Formal release staging only consumes this cache; it
// never falls back to the sibling builtin source collection.
type CacheStageOptions struct {
	CacheDir  string
	OutputDir string
	GOOS      string
	GOARCH    string
}

type CacheStageResult struct {
	CacheDir     string
	ManifestPath string
	Manifest     Manifest
}

// LoadManifest reads a generated builtins manifest.
func LoadManifest(manifestPath string) (Manifest, error) {
	info, err := os.Lstat(manifestPath)
	if err != nil {
		return Manifest{}, err
	}
	if !info.Mode().IsRegular() {
		return Manifest{}, fmt.Errorf("builtins manifest %s is not a regular file", manifestPath)
	}
	file, err := os.Open(manifestPath)
	if err != nil {
		return Manifest{}, err
	}
	defer file.Close()

	var manifest Manifest
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode builtins manifest: %w", err)
	}
	return manifest, nil
}

// VerifyManifest verifies every payload declared by a manifest below root.
// The manifest hash is the release contract for a locally built cache: local
// artifacts intentionally have a different archive hash from the canonical
// source lock used by sync-local-builtins.
func VerifyManifest(root string, manifest Manifest) error {
	if manifest.SchemaVersion != manifestSchemaVersion {
		return fmt.Errorf("unsupported builtins manifest schema %d", manifest.SchemaVersion)
	}
	if strings.TrimSpace(manifest.Platform.OS) == "" || strings.TrimSpace(manifest.Platform.Arch) == "" {
		return errors.New("builtins manifest platform is required")
	}
	if len(manifest.Components) == 0 {
		return errors.New("builtins manifest components are required")
	}

	seen := make(map[string]struct{}, len(manifest.Components))
	for _, component := range manifest.Components {
		if strings.TrimSpace(component.Name) == "" {
			return errors.New("builtins manifest component name is required")
		}
		if _, exists := seen[component.Name]; exists {
			return fmt.Errorf("duplicate builtins manifest component %q", component.Name)
		}
		seen[component.Name] = struct{}{}
		if err := validateManifestSHA256(component.SHA256); err != nil {
			return fmt.Errorf("builtins manifest component %s: %w", component.Name, err)
		}

		componentPath, err := cleanRelativePath(component.Path)
		if err != nil {
			return fmt.Errorf("builtins manifest component %s path: %w", component.Name, err)
		}
		if len(component.Tree) > 0 {
			if err := validateTreeLayout(TreeLayout{Root: "cache", Outputs: component.Tree}); err != nil {
				return fmt.Errorf("builtins manifest component %s tree: %w", component.Name, err)
			}
			firstPath, _ := cleanRelativePath(component.Tree[0].Path)
			if componentPath != firstPath {
				return fmt.Errorf("builtins manifest component %s path %q does not match tree entry %q", component.Name, component.Path, component.Tree[0].Path)
			}
			digest, err := TreeDigest(root, component.Tree)
			if err != nil {
				return fmt.Errorf("builtins manifest component %s tree: %w", component.Name, err)
			}
			if !strings.EqualFold(digest, component.SHA256) {
				return fmt.Errorf("builtins manifest component %s SHA-256 mismatch: manifest=%s actual=%s", component.Name, component.SHA256, digest)
			}
			continue
		}

		payloadPath, err := joinWithin(root, componentPath)
		if err != nil {
			return fmt.Errorf("builtins manifest component %s path: %w", component.Name, err)
		}
		info, err := os.Lstat(payloadPath)
		if err != nil {
			return fmt.Errorf("builtins manifest component %s payload: %w", component.Name, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("builtins manifest component %s payload is not a regular file", component.Name)
		}
		if err := verifyFileSHA256(payloadPath, component.SHA256); err != nil {
			return fmt.Errorf("builtins manifest component %s: %w", component.Name, err)
		}
	}
	return nil
}

// StageCache verifies and copies the complete target cache into a service
// bundle. Only package-owned cache subtrees are copied, so sibling source
// projects cannot become an implicit release input.
func StageCache(options CacheStageOptions) (CacheStageResult, error) {
	cacheDir, err := filepath.Abs(options.CacheDir)
	if err != nil {
		return CacheStageResult{}, err
	}
	outputDir, err := filepath.Abs(options.OutputDir)
	if err != nil {
		return CacheStageResult{}, err
	}
	if pathsOverlap(cacheDir, outputDir) {
		return CacheStageResult{}, errors.New("builtins cache and output directory must not overlap")
	}
	info, err := os.Lstat(cacheDir)
	if err != nil {
		return CacheStageResult{}, cachePreparationError(cacheDir, options.GOOS, options.GOARCH, err)
	}
	if !info.IsDir() {
		return CacheStageResult{}, cachePreparationError(cacheDir, options.GOOS, options.GOARCH, errors.New("cache path is not a directory"))
	}

	cacheManifestPath := filepath.Join(cacheDir, "builtins.manifest.json")
	manifest, err := LoadManifest(cacheManifestPath)
	if err != nil {
		return CacheStageResult{}, cachePreparationError(cacheDir, options.GOOS, options.GOARCH, err)
	}
	if manifest.Platform.OS != options.GOOS || manifest.Platform.Arch != options.GOARCH {
		return CacheStageResult{}, fmt.Errorf("local builtins cache platform %s/%s does not match target %s/%s", manifest.Platform.OS, manifest.Platform.Arch, options.GOOS, options.GOARCH)
	}
	if err := VerifyManifest(cacheDir, manifest); err != nil {
		return CacheStageResult{}, fmt.Errorf("local builtins cache verification failed: %w", err)
	}

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return CacheStageResult{}, err
	}
	for _, subtree := range []string{"bin", "libexec", "licenses", "sbom"} {
		source := filepath.Join(cacheDir, subtree)
		info, err := os.Lstat(source)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return CacheStageResult{}, err
		}
		if !info.IsDir() {
			return CacheStageResult{}, fmt.Errorf("local builtins cache %s is not a directory", source)
		}
		destination := filepath.Join(outputDir, subtree)
		if err := os.RemoveAll(destination); err != nil {
			return CacheStageResult{}, err
		}
		if err := copyCacheDirectory(source, destination); err != nil {
			return CacheStageResult{}, err
		}
	}
	destinationManifestPath := filepath.Join(outputDir, "builtins.manifest.json")
	if err := copyCacheFile(cacheManifestPath, destinationManifestPath); err != nil {
		return CacheStageResult{}, err
	}
	return CacheStageResult{
		CacheDir:     cacheDir,
		ManifestPath: destinationManifestPath,
		Manifest:     manifest,
	}, nil
}

func cachePreparationError(cacheDir, goos, goarch string, cause error) error {
	return fmt.Errorf("local builtins cache %s is unavailable; run ./scripts/sync-local-builtins.sh --target %s/%s first: %w", cacheDir, goos, goarch, cause)
}

func validateManifestSHA256(value string) error {
	if len(value) != sha256.Size*2 {
		return errors.New("SHA-256 must be 64 hexadecimal characters")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return errors.New("SHA-256 must be hexadecimal")
	}
	return nil
}

func sameCleanPath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	return strings.EqualFold(left, right)
}

func pathsOverlap(left, right string) bool {
	return sameCleanPath(left, right) || pathContains(left, right) || pathContains(right, left)
}

func pathContains(parent, candidate string) bool {
	relative, err := filepath.Rel(parent, candidate)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func copyCacheDirectory(source, destination string) error {
	return filepath.WalkDir(source, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("local builtins cache contains symbolic link %s", current)
		}
		relative, err := filepath.Rel(source, current)
		if err != nil {
			return err
		}
		target := destination
		if relative != "." {
			target = filepath.Join(destination, relative)
		}
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("local builtins cache contains unsupported entry %s", current)
		}
		return copyCacheFile(current, target)
	})
}

func copyCacheFile(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("local builtins cache file %s is not regular", source)
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Chmod(destination, info.Mode().Perm())
}
