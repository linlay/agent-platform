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
	if settings.Command == "" {
		return nil, fmt.Errorf("connector CLI declares no managed executable")
	}
	var entry string
	if pkg.Builtin {
		// Builtin packages are verified bundles that are never prepared.
		if entry, err = hostenv.LookPath(settings.Command, hostenv.Set(env, "PATH", pkg.BinDir)); err != nil {
			return nil, err
		}
	} else {
		installation, err := connector.ReadInstallation(pkg.InstallRoot, pkg.ID)
		if err != nil {
			return nil, ErrPreparationRequired
		}
		if bin := installation.Bin(); bin != "" {
			env = connector.WithPath(env, []string{bin})
		}
		// Run exactly the executable preparation verified, not a PATH lookup.
		if err := installation.VerifyExecutable(); err != nil {
			return nil, preparationRequiredError{err}
		}
		entry = installation.Executable
	}
	entry, err = filepath.EvalSymlinks(entry)
	if err != nil {
		return nil, err
	}
	if pkg.BinDir != "" {
		root, e := filepath.EvalSymlinks(pkg.Dir)
		if e != nil {
			return nil, e
		}
		rel, e := filepath.Rel(root, entry)
		if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("CLI escapes package")
		}
	}
	return executionCommand(ctx, entry, args, pkg.Dir, env, runtime.GOOS)
}

// ExecutionProgramCommand adapts a previously resolved trusted connector entry
// without adding shell interpretation or choosing an arbitrary caller program.
func ExecutionProgramCommand(ctx context.Context, entry string, args []string, dir string, env []string) (*exec.Cmd, error) {
	return executionCommand(ctx, entry, args, dir, env, runtime.GOOS)
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

func nodeShimScript(data string) (string, error) {
	return connector.WindowsNodeShimScript(data)
}
