package builtins

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/connector"
)

// PromoteConnectors validates complete immutable connector packages in a verified
// staging copy. Legacy executable-only caches must be rebuilt, never enriched
// with resources from the current Platform checkout.
func PromoteConnectors(root string, manifest Manifest) (Manifest, error) {
	for _, component := range manifest.Components {
		if component.Name != "dbx" && component.Name != "httpx" {
			continue
		}
		if err := validateConnectorComponent(root, component); err != nil {
			return Manifest{}, err
		}
	}
	// Desktop is embedded in Platform, not an external build artifact. Strip
	// the obsolete payload only from this already verified staging copy.
	filtered := manifest.Components[:0]
	for _, component := range manifest.Components {
		if component.Name != "desktop" {
			filtered = append(filtered, component)
		}
	}
	manifest.Components = filtered
	if err := os.RemoveAll(filepath.Join(root, "connectors", "builtin.desktop")); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func validateConnectorComponent(root string, component ManifestComponent) error {
	relative := "connectors/builtin." + component.Name
	if component.Path != relative || len(component.Tree) != 1 || component.Tree[0] != (TreeOutput{Path: relative, Type: "dir"}) {
		return fmt.Errorf("builtin %s needs a complete connector package; rebuild with sync-local-builtins", component.Name)
	}
	pkg, err := connector.Load(filepath.Join(root, "connectors"), "builtin."+component.Name)
	if err != nil {
		return err
	}
	if pkg.Version != strings.TrimPrefix(component.Version, "v") {
		return fmt.Errorf("builtin connector %s manifest version mismatch: connector.json=%q, builtins.manifest.json=%q", pkg.ID, pkg.Version, component.Version)
	}
	return nil
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
	verifiedPackages := map[string]bool{}
	for _, component := range manifest.Components {
		if component.Name != "dbx" && component.Name != "httpx" && component.Name != "desktop" {
			continue
		}
		id := "builtin." + component.Name
		verifiedPackages[id] = true
		if err := validateConnectorComponent(source, component); err != nil {
			return "", err
		}
		found = true
	}
	if !found {
		return "", nil
	}
	entries, err := os.ReadDir(filepath.Join(source, "connectors"))
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if !verifiedPackages[entry.Name()] {
			return "", fmt.Errorf("unverified builtin connector payload: %s", entry.Name())
		}
	}

	return filepath.Join(source, "connectors"), nil
}
