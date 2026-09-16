package connectorauth

import (
	"agent-platform/internal/connector"
	"agent-platform/internal/hostenv"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func quotePOSIX(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

// npmInitEnvironment leaves the declared init command unchanged. npm's --global
// is executed by a Platform shim whose final --prefix and config are private.
func npmInitEnvironment(pkg connector.Package, env []string, line string) ([]string, error) {
	if !strings.Contains(strings.ToLower(line), "npm") {
		return env, nil
	}
	actual, err := hostenv.LookPath("npm", env)
	if err != nil {
		return nil, fmt.Errorf("npm runtime is unavailable")
	}
	install, err := pkg.InstallDir()
	if err != nil {
		return nil, err
	}
	wrapperDir := filepath.Join(install, "setup", "npm-wrapper")
	if err = os.MkdirAll(wrapperDir, 0700); err != nil {
		return nil, err
	}
	userConfig := filepath.Join(install, "setup", "npm-user.config")
	globalConfig := filepath.Join(install, "setup", "npm-global.config")
	for _, path := range []string{userConfig, globalConfig} {
		if err = os.WriteFile(path, nil, 0600); err != nil {
			return nil, err
		}
	}
	name, script, err := npmShimScript(runtime.GOOS, actual, install)
	if err != nil {
		return nil, err
	}
	if err = os.WriteFile(filepath.Join(wrapperDir, name), []byte(script), 0700); err != nil {
		return nil, err
	}
	env = connector.WithPath(env, []string{wrapperDir})
	env = hostenv.Set(env, "NPM_CONFIG_PREFIX", install)
	env = hostenv.Set(env, "npm_config_prefix", install)
	env = hostenv.Set(env, "NPM_CONFIG_USERCONFIG", userConfig)
	env = hostenv.Set(env, "NPM_CONFIG_GLOBALCONFIG", globalConfig)
	env = hostenv.Set(env, "NPM_CONFIG_CACHE", filepath.Join(install, "setup", "cache", "npm"))
	return env, nil
}

// npm global places Windows command launchers at prefix root; expose only the
// declared executable through CONNECTOR_BIN_DIR, never by scanning the prefix.
func exposePrivateNPMEntry(pkg connector.Package, command string) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	install, err := pkg.InstallDir()
	if err != nil {
		return err
	}
	entry := filepath.Join(install, command+".cmd")
	info, err := os.Lstat(entry)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("invalid private npm entry")
	}
	if strings.ContainsAny(entry, "\r\n\"%!&|<>^") {
		return fmt.Errorf("unsupported npm private path")
	}
	bin := filepath.Join(install, "bin")
	if err = os.MkdirAll(bin, 0700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(bin, command+".cmd"), []byte("@echo off\r\ncall \""+entry+"\" %*\r\nexit /b %errorlevel%\r\n"), 0700)
}

func npmShimScript(goos, actual, install string) (string, string, error) {
	var script, name string
	if goos == "windows" {
		// These paths are Platform derived; cmd metacharacters are rejected before
		// writing the wrapper instead of risking another command through % expansion.
		if strings.ContainsAny(actual+install, "\r\n\"%!&|<>^") {
			return "", "", fmt.Errorf("unsupported npm private path")
		}
		name = "npm.cmd"
		script = "@echo off\r\ncall \"" + actual + "\" %* --prefix \"" + install + "\"\r\nexit /b %errorlevel%\r\n"
	} else {
		name = "npm"
		script = "#!/bin/sh\nexec " + quotePOSIX(actual) + " \"$@\" --prefix " + quotePOSIX(install) + "\n"
	}
	return name, script, nil
}
