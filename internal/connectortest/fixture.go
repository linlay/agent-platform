// Package connectortest creates synthetic connector packages for contract tests.
// It deliberately contains no released CLI skills or project resources.
package connectortest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func WriteCLI(dir, name, version, goos string) error {
	version = strings.TrimPrefix(version, "v")
	files := map[string]string{
		"connector.json":                       fmt.Sprintf("{\n  \"id\": \"builtin.%s\",\n  \"name\": \"%s\",\n  \"version\": \"%s\",\n  \"type\": \"cli\",\n  \"auth_mode\": null\n}\n", name, name, version),
		"cli.json":                             fmt.Sprintf(`{"versionCheck":{"command":{"darwin":"%s --version","linux":"%s --version","win32":"%s.exe --version"},"minVersion":"0.1.0","versionPattern":"v?([0-9]+\\.[0-9]+\\.[0-9]+)"}}`, name, name, name),
		"skills/builtin-" + name + "/SKILL.md": "---\nname: builtin-" + name + "\ndescription: Synthetic CLI contract fixture\n---\n\nFixture instructions.\n",
		"skills/builtin-" + name + "/references/commands.md": "Synthetic command reference.\n",
	}
	for rel, data := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(data), 0644); err != nil {
			return err
		}
	}
	return os.MkdirAll(filepath.Join(dir, "bin", "libs"), 0755)
}
