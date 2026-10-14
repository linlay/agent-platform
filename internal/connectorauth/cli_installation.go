package connectorauth

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"agent-platform/internal/connector"
	"agent-platform/internal/hostenv"
)

// CLIInstallationBinDir identifies a package-version-specific installation,
// outside the immutable package and outside its private login directories.
// InstallationDigest keeps source and materialized packages on the same entry.
func CLIInstallationBinDir(pkg connector.Package) (string, error) {
	installation := pkg
	if pkg.InstallationRoot != "" {
		installation.StateRoot = pkg.InstallationRoot
	}
	root, err := installation.ConnectorStateDir()
	if err != nil {
		return "", err
	}
	version := pkg.Version
	if version == "" || filepath.Base(version) != version || strings.ContainsAny(version, "/\\") {
		return "", fmt.Errorf("CLI installation requires a package version")
	}
	key := pkg.InstallationDigest
	if key == "" {
		key, err = connector.RuntimeFingerprint(pkg.Dir)
		if err != nil {
			return "", err
		}
	}
	if len(key) != 64 || strings.Trim(key, "0123456789abcdef") != "" {
		return "", fmt.Errorf("invalid CLI installation digest")
	}
	return filepath.Join(root, "installations", version, key, "bin"), nil
}

// CLIInstallationEnvironment also serves business calls and stdio MCP. It adds
// public paths only; callers resolve credentials separately for their phase.
func CLIInstallationEnvironment(pkg connector.Package, env []string) ([]string, error) {
	bin, err := CLIInstallationBinDir(pkg)
	if err != nil {
		return nil, err
	}
	arch := runtime.GOARCH
	if arch != "arm64" && arch != "amd64" {
		return nil, fmt.Errorf("UNSUPPORTED_CAPABILITY: unsupported connector architecture")
	}
	env = hostenv.Set(env, "CONNECTOR_BIN_DIR", bin)
	env = hostenv.Set(env, "CONNECTOR_ARCH", arch)
	dirs := []string{bin}
	if pkg.BinDir != "" {
		dirs = append(dirs, pkg.BinDir)
	}
	env = connector.WithPath(env, dirs)
	return env, nil
}

// CLIPrivateEnvironmentDirs lists only the connector's login/configuration
// directories. Preparation state, installed programs and invocation receipts
// live outside this set and must survive sign-out.
func CLIPrivateEnvironmentDirs(pkg connector.Package) (map[string]string, error) {
	state, err := pkg.ConnectorStateDir()
	if err != nil {
		return nil, err
	}
	result := make(map[string]string)
	for key, name := range map[string]string{"HOME": "home", "USERPROFILE": "home", "XDG_CONFIG_HOME": "config", "XDG_CACHE_HOME": "cache", "XDG_DATA_HOME": "data", "XDG_STATE_HOME": "state", "TMPDIR": "tmp", "TMP": "tmp", "TEMP": "tmp"} {
		result[key] = filepath.Join(state, name)
	}
	if runtime.GOOS == "windows" {
		result["APPDATA"] = filepath.Join(state, "config")
		result["LOCALAPPDATA"] = filepath.Join(state, "data")
	}
	return result, nil
}

// Lifecycle and business calls share the same private directory preparation.
func applyCLIPrivateEnvironment(pkg connector.Package, env []string) ([]string, error) {
	state, err := pkg.ConnectorStateDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(state), 0700); err != nil {
		return nil, err
	}
	if err := preparePrivateDirectories(filepath.Dir(state), filepath.Base(state)); err != nil {
		return nil, err
	}
	if err := preparePrivateDirectories(state, "home", "config", "cache", "data", "state", "tmp"); err != nil {
		return nil, err
	}
	dirs, err := CLIPrivateEnvironmentDirs(pkg)
	if err != nil {
		return nil, err
	}
	for key, path := range dirs {
		env = hostenv.Set(env, key, path)
	}
	return env, nil
}

// preparePrivateDirectories refuses a link at any platform-owned path. npm may
// create relative links inside the resulting installation, but cannot redirect
// the installation's root through an existing symlink.
func preparePrivateDirectories(root string, names ...string) error {
	for _, name := range append([]string{""}, names...) {
		path := filepath.Join(root, name)
		for current := root; ; {
			if info, err := os.Lstat(current); err == nil {
				if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
					return fmt.Errorf("CLI private path must be a real directory")
				}
			} else if os.IsNotExist(err) {
				if err := os.Mkdir(current, 0700); err != nil && !os.IsExist(err) {
					return err
				}
			} else {
				return err
			}
			if current == path {
				break
			}
			rel, err := filepath.Rel(current, path)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return fmt.Errorf("CLI private path escapes its root")
			}
			current = filepath.Join(current, strings.Split(rel, string(filepath.Separator))[0])
		}
	}
	return nil
}
