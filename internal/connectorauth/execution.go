package connectorauth

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"agent-platform/internal/builtins"
	"agent-platform/internal/connector"
	"agent-platform/internal/hostenv"
)

// ExecutionCommand resolves the installed connector command, never a caller path.
// It does not prepare/install software or inherit the lifecycle's ambient secrets.
func ExecutionCommand(ctx context.Context, pkg connector.Package, args, env []string) (*exec.Cmd, error) {
	env = builtins.EnsureBinInEnv(env)
	settings, err := cliSettingsFor(pkg)
	if err != nil {
		return nil, err
	}
	env, err = CLIInstallationEnvironment(pkg, env)
	if err != nil {
		return nil, err
	}
	entry, err := hostenv.LookPath(settings.Command, env)
	if err != nil {
		return nil, err
	}
	lookupEntry := entry
	entry, err = filepath.EvalSymlinks(entry)
	if err != nil {
		return nil, err
	}
	installationBin := hostenv.Value(env, "CONNECTOR_BIN_DIR")
	ownedRoot := ""
	if pathInside(installationBin, lookupEntry) {
		ownedRoot = filepath.Dir(installationBin)
	} else if pkg.BinDir != "" && pathInside(pkg.BinDir, lookupEntry) {
		ownedRoot = pkg.Dir
	}
	if ownedRoot != "" {
		root, e := filepath.EvalSymlinks(ownedRoot)
		if e != nil {
			return nil, e
		}
		rel, e := filepath.Rel(root, entry)
		if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("CLI escapes its package installation")
		}
	}
	return executionCommand(ctx, entry, args, pkg.Dir, env, runtime.GOOS)
}

// ExecutionProgramCommand adapts a previously resolved trusted component entry
// without adding shell interpretation or choosing an arbitrary caller program.
func ExecutionProgramCommand(ctx context.Context, entry string, args []string, dir string, env []string) (*exec.Cmd, error) {
	return executionCommand(ctx, entry, args, dir, env, runtime.GOOS)
}

func pathInside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func executionCommand(ctx context.Context, entry string, args []string, dir string, env []string, goos string) (*exec.Cmd, error) {
	ext := strings.ToLower(filepath.Ext(entry))
	if goos == "windows" && (ext == ".cmd" || ext == ".bat") {
		// Resolve standard Node shims to their script; never transport user argv via cmd.exe.
		target, err := connector.WindowsNodeShimTarget(entry)
		if err != nil {
			return nil, err
		}
		entry = target
		ext = strings.ToLower(filepath.Ext(entry))
	} else if goos == "windows" && ext != ".exe" && ext != ".com" && ext != ".js" && ext != ".cjs" && ext != ".mjs" {
		return nil, fmt.Errorf("unsupported Windows CLI launcher")
	}
	if ext == ".js" || ext == ".mjs" || ext == ".cjs" {
		node, err := hostenv.LookPath("node", env)
		if err != nil {
			return nil, err
		}
		args = append([]string{entry}, args...)
		entry = node
	}
	cmd := exec.CommandContext(ctx, entry, args...)
	cmd.Dir = dir
	cmd.Env = env
	configureProcess(cmd)
	return cmd, nil
}
