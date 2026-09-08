package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/config"
	"agent-platform/internal/connector"
)

// Legacy snippets remain useful as migration fixtures. The runtime under test
// receives only connector.json/mcp.json; no YAML registry is loaded.
func writeConnectorFixture(path string, data []byte, mode os.FileMode) error {
	if filepath.Ext(path) != ".yml" && filepath.Ext(path) != ".yaml" {
		return os.WriteFile(path, data, mode)
	}
	if strings.Contains(filepath.Base(path), ".example.") {
		return nil
	}
	root := filepath.Dir(path)
	tree, err := config.LoadYAMLTreeBytes(data)
	if err != nil {
		return err
	}
	manifest, component, credentials, err := ConvertLegacy(path, tree)
	if err != nil {
		id := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		dir := filepath.Join(root, id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, "connector.json"), []byte("invalid: "+err.Error()), 0o644)
	}
	dir := filepath.Join(root, manifest.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, value := range map[string]any{"connector.json": manifest, "mcp.json": component} {
		bytes, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, name), bytes, mode); err != nil {
			return err
		}
	}
	if len(credentials) > 0 {
		credentialPath, err := connector.CredentialsPath((connector.Sources{ExternalRoot: root}).PersistentRoot(), manifest.ID)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(credentialPath), 0o700); err != nil {
			return err
		}
		bytes, _ := json.Marshal(credentials)
		if err := os.WriteFile(credentialPath, bytes, 0o600); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(root, ".fixture-"+filepath.Base(path)), []byte(manifest.ID), 0o600)
}

func removeConnectorFixture(path string) error {
	marker := filepath.Join(filepath.Dir(path), ".fixture-"+filepath.Base(path))
	id, err := os.ReadFile(marker)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(filepath.Dir(path), string(id))); err != nil {
		return err
	}
	return os.Remove(marker)
}
