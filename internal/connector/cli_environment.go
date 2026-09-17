package connector

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// CLIConfigEnvironment contains paths only, never credential contents.
func (p Package) CLIConfigEnvironment() (map[string]string, error) {
	platform, _ := p.CLI["platform"].(map[string]any)
	raw, exists := platform["configEnv"]
	if !exists {
		return nil, nil
	}
	name, ok := raw.(string)
	if !ok || !regexp.MustCompile(`^[A-Z][A-Z0-9_]*_CONFIG_DIR$`).MatchString(name) || strings.HasPrefix(name, "AP_") {
		return nil, fmt.Errorf("invalid CLI configEnv")
	}
	dir, err := StateDir(p.PersistentRoot(), p.ID)
	if err != nil {
		return nil, err
	}
	return map[string]string{name: filepath.Join(dir, "config")}, nil
}
