package connectorauth

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	lookup := env
	if pkg.BinDir != "" {
		lookup = hostenv.Set(env, "PATH", pkg.BinDir)
	}
	entry, err := hostenv.LookPath(settings.Command, lookup)
	if err != nil {
		return nil, err
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

func executionCommand(ctx context.Context, entry string, args []string, dir string, env []string, goos string) (*exec.Cmd, error) {
	ext := strings.ToLower(filepath.Ext(entry))
	if goos == "windows" && (ext == ".cmd" || ext == ".bat") {
		// Resolve standard Node shims to their script; never transport user argv via cmd.exe.
		data, err := os.ReadFile(entry)
		if err != nil || len(data) > 65536 {
			return nil, fmt.Errorf("unsupported CLI launcher")
		}
		script, err := nodeShimScript(string(data))
		if err != nil {
			return nil, err
		}
		target := filepath.Join(filepath.Dir(entry), filepath.FromSlash(strings.ReplaceAll(script, "\\", "/")))
		target, err = filepath.EvalSymlinks(target)
		if err != nil {
			return nil, err
		}
		root, err := filepath.EvalSymlinks(filepath.Dir(entry))
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(root, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("CLI shim escapes installation")
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

var nodeShimPattern = regexp.MustCompile(`(?i)"%(?:~dp0|dp0%)[\\/]?([^"\r\n]+\.(?:js|cjs|mjs))"`)

func nodeShimScript(data string) (string, error) {
	matches := nodeShimPattern.FindAllStringSubmatch(data, -1)
	target := ""
	for _, m := range matches {
		if strings.ContainsAny(m[1], "%!\x00") || (target != "" && target != m[1]) {
			return "", fmt.Errorf("ambiguous CLI shim")
		}
		target = m[1]
	}
	if target == "" {
		return "", fmt.Errorf("unsupported CLI shim")
	}
	return target, nil
}
