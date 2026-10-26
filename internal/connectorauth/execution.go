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
// The caller holds the returned release until the command exits: it keeps the
// installation from collection when a preparation activates a newer one.
func ExecutionCommand(ctx context.Context, pkg connector.Package, args, env []string) (*exec.Cmd, func(), error) {
	env = builtins.EnsureBinInEnv(env)
	settings, err := cliSettingsFor(pkg)
	if err != nil {
		return nil, nil, err
	}
	if settings.Command == "" {
		return nil, nil, fmt.Errorf("connector CLI declares no managed executable")
	}
	var entry string
	release := func() {}
	if pkg.Builtin {
		// Builtin packages are verified bundles that are never prepared.
		if entry, err = hostenv.LookPath(settings.Command, hostenv.Set(env, "PATH", pkg.BinDir)); err != nil {
			return nil, nil, err
		}
	} else {
		var installation connector.Installation
		installation, release, err = retainActiveInstallation(pkg)
		if err != nil {
			return nil, nil, err
		}
		if bin := installation.Bin(); bin != "" {
			env = connector.WithPath(env, []string{bin})
		}
		entry = installation.Executable
	}
	cmd, err := resolvedCommand(ctx, pkg, entry, args, env)
	if err != nil {
		release()
		return nil, nil, err
	}
	return cmd, release, nil
}

// retainActiveInstallation leases the active installation and then verifies
// exactly the executable preparation pinned, not a PATH lookup. Collection may
// remove an installation a newer preparation just replaced between the read and
// the lease, so the read is retried once.
func retainActiveInstallation(pkg connector.Package) (connector.Installation, func(), error) {
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		installation, err := connector.ReadInstallation(pkg.InstallRoot, pkg.ID)
		if err != nil {
			return installation, nil, ErrPreparationRequired
		}
		release := func() {}
		if installation.Prefix != "" {
			if release, err = connector.RetainInstallation(installation.Prefix); err != nil {
				last = err
				continue
			}
		}
		if err := installation.VerifyExecutable(); err != nil {
			release()
			last = preparationRequiredError{err}
			continue
		}
		return installation, release, nil
	}
	return connector.Installation{}, nil, last
}

func resolvedCommand(ctx context.Context, pkg connector.Package, entry string, args, env []string) (*exec.Cmd, error) {
	entry, err := filepath.EvalSymlinks(entry)
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
