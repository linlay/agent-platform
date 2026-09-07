package connector

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"agent-platform/internal/resources"
)

// WriteBuiltin stages the complete bundled resources beside a verified native
// executable. Callers publish and checksum the resulting package atomically.
func WriteBuiltin(dir, name, version, goos string) error {
	id := "builtin." + name
	source := path.Join("connectors", id)
	if name != "dbx" && name != "httpx" {
		return fmt.Errorf("unknown builtin connector %q", name)
	}
	// Remove obsolete bundled skills when refreshing an older verified cache.
	if err := os.RemoveAll(filepath.Join(dir, "skills")); err != nil {
		return err
	}
	if err := fs.WalkDir(resources.ConnectorFS, source, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Name() == ".DS_Store" || entry.Name() == "Thumbs.db" {
			return nil
		}
		relative := strings.TrimPrefix(strings.TrimPrefix(file, source), "/")
		dest := filepath.Join(dir, filepath.FromSlash(relative))
		if entry.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		data, err := resources.ConnectorFS.ReadFile(file)
		if err != nil {
			return err
		}
		if relative == "connector.json" {
			var manifest Manifest
			if err := DecodeJSON(data, &manifest); err != nil {
				return err
			}
			manifest.Version = strings.TrimPrefix(version, "v")
			if err := validateManifest(id, manifest); err != nil {
				return err
			}
			data, err = json.MarshalIndent(manifest, "", "  ")
			if err != nil {
				return err
			}
			data = append(data, '\n')
		}
		return os.WriteFile(dest, data, 0o644)
	}); err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(dir, "bin", "libs"), 0o755)
}

// BuiltinSkillConnector identifies the retired standalone copies of bundled
// skills. These names cannot grant a connector through mustUseSkills.
func BuiltinSkillConnector(name string) string {
	switch strings.ToLower(name) {
	case "builtin-dbx":
		return "builtin.dbx"
	case "builtin-httpx":
		return "builtin.httpx"
	default:
		return ""
	}
}

func IsReservedSkill(name string) bool {
	return strings.HasPrefix(strings.ToLower(name), "connector-") || BuiltinSkillConnector(name) != ""
}
