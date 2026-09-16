package connectorauth

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"agent-platform/internal/bashast"
	"agent-platform/internal/builtins"
	"agent-platform/internal/connector"
	"agent-platform/internal/hostenv"
)

func validatePrivateCLIInit(line string) error {
	if strings.Contains(strings.ToLower(line), "@latest") {
		return fmt.Errorf("npm CLI init requires an exact package version")
	}
	// --global is a package declaration, not permission to use the user's prefix.
	// The Platform-owned npm shim appends its private --prefix at execution time.
	if regexp.MustCompile(`(?i)\bnpm(?:\.cmd)?\s+(?:install|i|add)\b`).MatchString(line) && !regexp.MustCompile(`@[0-9]+\.[0-9]+\.[0-9]+(?:[-+][a-zA-Z0-9.-]+)?(?:[\s"';]|$)`).MatchString(line) {
		return fmt.Errorf("npm CLI init requires an exact package version")
	}

	for _, call := range bashast.ParseForSecurity(line).Commands {
		if len(call.Argv) == 0 || strings.TrimSuffix(strings.ToLower(filepath.Base(call.Argv[0])), ".cmd") != "npm" {
			continue
		}
		installing := false
		packages := 0
		for i := 1; i < len(call.Argv); i++ {
			arg := call.Argv[i]
			if !installing {
				if arg == "install" || arg == "i" || arg == "add" {
					installing = true
				}
				continue
			}
			if arg == "--prefix" || arg == "--cache" || arg == "--userconfig" || arg == "--globalconfig" {
				i++
				continue
			}
			if strings.HasPrefix(arg, "-") {
				continue
			}
			at := strings.LastIndex(arg, "@")
			if at <= 0 || !validCLISemVer(arg[at+1:]) {
				return fmt.Errorf("npm CLI init requires an exact version for every package")
			}
			packages++
		}
		if installing && packages == 0 {
			return fmt.Errorf("npm CLI init requires an explicitly versioned package")
		}
	}
	return nil
}

func (m *Manager) cliPrivateEntry(pkg connector.Package, command string) (string, error) {
	dirs, err := pkg.CLIBinDirs()
	if err != nil {
		return "", err
	}
	env := hostenv.Set(os.Environ(), "PATH", strings.Join(dirs, string(os.PathListSeparator)))
	entry, err := hostenv.LookPath(command, env)
	if err != nil {
		if pkg.CLI["init"] == nil {
			// Spec 4.7 permits a declared host CLI when no installer is supplied.
			// It is still version checked and its exact entry/hash are frozen.
			return hostenv.LookPath(command, builtins.EnsureBinInEnv(os.Environ()))
		}
		return "", fmt.Errorf("CLI executable is missing from connector-private installation: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(entry)
	if err != nil {
		return "", err
	}
	root, err := pkg.InstallDir()
	if err != nil {
		return "", err
	}
	roots := []string{root}
	if pkg.BinDir != "" {
		roots = append(roots, pkg.Dir)
	}
	for _, candidate := range roots {
		canonical, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(canonical, resolved)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel) {
			return entry, nil
		}
	}
	return "", fmt.Errorf("CLI executable escapes its connector-private installation")

}
