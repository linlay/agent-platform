package builtins

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BundleGitBashFromEnv is a build-time selection, independent of runtime config.
func BundleGitBashFromEnv() (bool, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("BUNDLE_GIT_BASH"))) {
	case "", "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("BUNDLE_GIT_BASH must be true or false")
	}
}

func removeExcludedGitBash(root string) error {
	for _, dir := range []string{"libexec", "licenses", "sbom"} {
		if err := os.RemoveAll(filepath.Join(root, dir, GitBashComponent)); err != nil {
			return err
		}
	}
	return nil
}

// VerifyPlatformSelection validates a finished bundle, not a source cache being filtered.
func VerifyPlatformSelection(root string, manifest Manifest) error {
	if err := RequirePlatformComponents(manifest); err != nil {
		return err
	}
	if manifest.GitBashExcluded {
		for _, dir := range []string{"libexec", "licenses", "sbom"} {
			_, err := os.Lstat(filepath.Join(root, dir, GitBashComponent))
			if !os.IsNotExist(err) {
				return fmt.Errorf("excluded git-bash path still present or inaccessible: %s", dir)
			}
		}
	}
	return nil
}

func selectGitBash(manifest Manifest, exclude bool) Manifest {
	manifest.GitBashExcluded = exclude
	if exclude {
		components := make([]ManifestComponent, 0, len(manifest.Components))
		for _, component := range manifest.Components {
			if component.Name != GitBashComponent {
				components = append(components, component)
			}
		}
		manifest.Components = components
	}
	return manifest
}
