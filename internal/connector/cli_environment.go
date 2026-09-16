package connector

import (
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// CLIBinDirs orders only package-owned executable paths. System PATH may provide
// runtimes (node, npm, sh), but cannot satisfy prepared connector verification.
func (p Package) CLIBinDirs() ([]string, error) {
	if p.Builtin {
		return []string{p.BinDir}, nil
	}
	dir, err := p.InstallDir()
	if err != nil {
		return nil, err
	}
	bins := []string{filepath.Join(dir, "bin"), filepath.Join(dir, "node_modules", ".bin")}
	if p.BinDir != "" {
		bins = append(bins, p.BinDir)
	}
	return bins, nil
}

// CLIConfigEnvironment contains private paths only, never credential contents or
// dynamic cli.json env commands. Auth and init must not run dynamic env hooks.
func (p Package) CLIConfigEnvironment() (map[string]string, error) {
	// Catalog assembly has no authenticated subject and must not merge private
	// HOME/XDG paths from multiple connectors into one Agent-wide environment.
	if p.Owner == "" || p.CLI == nil {
		return nil, nil
	}
	return p.CLIPrivateEnvironment()
}

// CLIPrivateEnvironment is for one selected connector lifecycle/execution only.
// Standalone managers without an HTTP subject retain their local state root.
func (p Package) CLIPrivateEnvironment() (map[string]string, error) {
	// Bundled tools have no external per-user credential/install lifecycle.
	// Validation also loads them before an owner or an Agent exists.
	if p.Builtin || IsBuiltin(p.ID) {
		return nil, nil
	}
	dir, err := p.UserStateDir()
	if err != nil {
		return nil, err
	}
	install, err := p.InstallDir()
	if err != nil {
		return nil, err
	}
	values := map[string]string{
		"HOME":                  filepath.Join(dir, "home"),
		"XDG_CONFIG_HOME":       filepath.Join(dir, "config"),
		"XDG_CACHE_HOME":        filepath.Join(dir, "cache"),
		"XDG_DATA_HOME":         filepath.Join(dir, "data"),
		"XDG_STATE_HOME":        filepath.Join(dir, "state"),
		"CONNECTOR_BIN_DIR":     filepath.Join(install, "bin"),
		"CONNECTOR_INSTALL_DIR": install,
		"CONNECTOR_ARCH":        runtime.GOARCH,
	}
	if runtime.GOOS == "windows" {
		values["USERPROFILE"] = values["HOME"]
		values["APPDATA"] = values["XDG_CONFIG_HOME"]
		values["LOCALAPPDATA"] = values["XDG_DATA_HOME"]
	}
	platform, _ := p.CLI["platform"].(map[string]any)
	raw, exists := platform["configEnv"]
	if !exists {
		return values, nil
	}
	name, ok := raw.(string)
	if !ok || !regexp.MustCompile(`^[A-Z][A-Z0-9_]*_CONFIG_DIR$`).MatchString(name) || strings.HasPrefix(name, "AP_") {
		return nil, fmt.Errorf("invalid CLI configEnv")
	}
	values[name] = filepath.Join(dir, "config")
	return values, nil
}
