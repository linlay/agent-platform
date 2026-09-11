package builtins

import (
	"fmt"
	"os"
	"path/filepath"
)

const GitBashComponent = "git-bash"
const GitBashRelativeRoot = "libexec/git-bash/windows-amd64"

// ProcessBinDir is the already resolved bundle/cache bin directory, not PATH.
func ProcessBinDir() string {
	processBinState.RLock()
	defer processBinState.RUnlock()
	return processBinState.dir
}

// ResolveGitBash verifies the complete component before returning its root.
// Searching PATH or a system Git installation would violate bundle provenance.
func ResolveGitBash(goos, goarch string) (string, error) {
	if goos != "windows" || goarch != "amd64" {
		return "", fmt.Errorf("git_bash_unsupported: supported target is windows/amd64, got %s/%s", goos, goarch)
	}
	bin := ProcessBinDir()
	if bin == "" {
		return "", fmt.Errorf("git_bash_unavailable: builtin bin directory is not configured")
	}
	return verifyGitBashAt(filepath.Dir(bin))
}

func verifyGitBashAt(root string) (string, error) {
	manifest, err := LoadManifest(filepath.Join(root, "builtins.manifest.json"))
	if err != nil {
		return "", fmt.Errorf("git_bash_unavailable: %w", err)
	}
	if manifest.Platform.OS != "windows" || manifest.Platform.Arch != "amd64" {
		return "", fmt.Errorf("git_bash_unavailable: builtin manifest is not windows/amd64")
	}
	var selected []ManifestComponent
	for _, component := range manifest.Components {
		if component.Name == GitBashComponent {
			selected = append(selected, component)
		}
	}
	if len(selected) != 1 || selected[0].Path != GitBashRelativeRoot ||
		len(selected[0].Tree) != 1 || selected[0].Tree[0].Path != GitBashRelativeRoot || selected[0].Tree[0].Type != "dir" {
		return "", fmt.Errorf("git_bash_unavailable: missing or invalid git-bash archive-tree; prepare the Windows builtin cache")
	}
	manifest.Components = selected
	if err := VerifyManifest(root, manifest); err != nil {
		return "", fmt.Errorf("git_bash_unavailable: %w", err)
	}
	runtimeRoot := filepath.Join(root, filepath.FromSlash(GitBashRelativeRoot))
	for _, name := range []string{"usr/bin/bash.exe", "usr/bin/cygpath.exe", "usr/bin/msys-2.0.dll", "mingw64/bin/git.exe", "etc/package-versions.txt", "LICENSE.txt"} {
		info, err := os.Lstat(filepath.Join(runtimeRoot, filepath.FromSlash(name)))
		if err != nil || !info.Mode().IsRegular() {
			return "", fmt.Errorf("git_bash_unavailable: required runtime file %s is missing or not regular", name)
		}
	}
	return runtimeRoot, nil
}

// RequirePlatformComponents is a release completeness gate in addition to
// checksum verification (which alone cannot detect an omitted component).
func RequirePlatformComponents(manifest Manifest) error {
	if manifest.GitBashExcluded {
		for _, component := range manifest.Components {
			if component.Name == GitBashComponent {
				return fmt.Errorf("git-bash is both excluded and present in builtin manifest")
			}
		}
		return nil
	}
	if manifest.Platform.OS != "windows" || manifest.Platform.Arch != "amd64" {
		return nil
	}
	for _, component := range manifest.Components {
		if component.Name == GitBashComponent && component.Path == GitBashRelativeRoot && len(component.Tree) == 1 &&
			component.Tree[0].Path == GitBashRelativeRoot && component.Tree[0].Type == "dir" {
			return nil
		}
	}
	return fmt.Errorf("Windows/amd64 release requires the git-bash builtin; rebuild the builtin cache")
}
