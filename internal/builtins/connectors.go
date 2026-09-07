package builtins

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/connector"
)

// PromoteConnectors refreshes bundled resources in an already verified cache
// copy, upgrading legacy binaries to connector layout when necessary. It uses
// existing executables without rebuilding Rust or CLI projects. The returned
// manifest hashes the complete package trees.
func PromoteConnectors(root string, manifest Manifest) (Manifest, error) {
	for i, component := range manifest.Components {
		if component.Name != "dbx" && component.Name != "httpx" {
			continue
		}
		relative := filepath.ToSlash(filepath.Join("connectors", "builtin."+component.Name))
		legacy := component.Path != relative
		if legacy && len(component.Tree) != 0 {
			return Manifest{}, fmt.Errorf("unexpected legacy connector payload layout for %s", component.Name)
		}
		entry := component.Name
		if manifest.Platform.OS == "windows" {
			entry += ".exe"
		}
		dir := filepath.Join(root, filepath.FromSlash(relative))
		if err := connector.WriteBuiltin(dir, component.Name, component.Version, manifest.Platform.OS); err != nil {
			return Manifest{}, err
		}
		if legacy {
			if err := copyCacheFile(filepath.Join(root, filepath.FromSlash(component.Path)), filepath.Join(dir, "bin", entry)); err != nil {
				return Manifest{}, err
			}
			if err := os.Remove(filepath.Join(root, filepath.FromSlash(component.Path))); err != nil {
				return Manifest{}, err
			}
		}
		component.Path = relative
		component.Tree = []TreeOutput{{Path: relative, Type: "dir"}}
		var err error
		component.SHA256, err = TreeDigest(root, component.Tree)
		if err != nil {
			return Manifest{}, err
		}
		manifest.Components[i] = component
	}
	return manifest, nil
}

// ProcessConnectorsRoot verifies the platform bundle and returns its immutable
// connector source. It never installs or updates anything in the runtime root.
func ProcessConnectorsRoot() (string, error) {
	processBinState.RLock()
	bin := processBinState.dir
	processBinState.RUnlock()
	if bin == "" {
		return "", nil
	}
	source := filepath.Dir(bin)
	manifest, err := LoadManifest(filepath.Join(source, "builtins.manifest.json"))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if err := VerifyManifest(source, manifest); err != nil {
		return "", err
	}
	found := false
	for _, component := range manifest.Components {
		if component.Name != "dbx" && component.Name != "httpx" {
			continue
		}
		id := "builtin." + component.Name
		relative := "connectors/" + id
		if component.Path != relative || len(component.Tree) != 1 || component.Tree[0] != (TreeOutput{Path: relative, Type: "dir"}) {
			return "", fmt.Errorf("builtin %s needs a connector package; prepare the cache with sync-local-builtins", component.Name)
		}
		pkg, err := connector.Load(filepath.Join(source, "connectors"), id)
		if err != nil {
			return "", err
		}
		// WriteBuiltin removes the release tag's optional v prefix when
		// producing the SemVer required by connector.json.
		if pkg.Version != strings.TrimPrefix(component.Version, "v") {
			return "", fmt.Errorf("builtin connector %s manifest version mismatch: connector.json=%q, builtins.manifest.json=%q", id, pkg.Version, component.Version)
		}
		found = true
	}
	if !found {
		return "", nil
	}
	return filepath.Join(source, "connectors"), nil
}
