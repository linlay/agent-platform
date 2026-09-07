package hostshell

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"agent-platform/internal/processgroup"
)

// Paths interprets MSYS paths using the SAME bundle, environment and cwd as
// execution. It returns lexical Windows paths; AccessPolicy must still perform
// canonical/symlink/junction checks without losing temporary-root provenance.
type Paths struct {
	Context context.Context
	Root    string
	Env     []string
}

func (p Paths) ResolveMSYS(raw, cwd string) (string, error) {
	if strings.TrimSpace(raw) == "" || strings.ContainsAny(raw, "\x00\r\n") {
		return "", fmt.Errorf("invalid MSYS path")
	}
	if raw == "~" || strings.HasPrefix(raw, "~/") {
		raw = filepath.Join(envValue(p.Env, "HOME"), strings.TrimPrefix(strings.TrimPrefix(raw, "~"), "/"))
	} else if strings.HasPrefix(raw, "~") {
		return "", fmt.Errorf("named-user home expansion is not supported; use an absolute path")
	}
	if strings.HasPrefix(raw, "/dev/") || strings.HasPrefix(raw, "/proc/") {
		return "", fmt.Errorf("MSYS virtual path cannot be authorized as a host file")
	}
	ctx := p.Context
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(p.Root, "usr", "bin", "cygpath.exe"), "-am", "--", raw)
	cmd.Dir, cmd.Env = cwd, p.Env
	var stdout strings.Builder
	cmd.Stdout = &stdout
	if err := processgroup.Run(cmd); err != nil {
		return "", fmt.Errorf("cannot resolve MSYS path: %w", err)
	}
	resolved := strings.TrimSuffix(strings.TrimSuffix(stdout.String(), "\n"), "\r")
	if resolved == "" || strings.ContainsAny(resolved, "\r\n\x00") || !filepath.IsAbs(resolved) {
		return "", fmt.Errorf("cygpath did not return one absolute Windows path")
	}
	return filepath.Clean(resolved), nil
}

func (p Paths) CommandUsesMSYS(command, cwd string) (bool, error) {
	base := CommandBase(command, true)
	if !strings.ContainsAny(command, `/\`) {
		switch base {
		case "echo", "printf", "pwd", "true", "false", "test", "[":
			return true, nil // Bash builtins; profiles/functions are disabled.
		}
	}
	var candidates []string
	if strings.ContainsAny(command, `/\`) {
		resolved, err := p.ResolveMSYS(command, cwd)
		if err != nil {
			return false, err
		}
		candidates = []string{resolved}
	} else {
		for _, dir := range strings.Split(envValue(p.Env, "PATH"), ";") {
			if dir != "" {
				candidates = append(candidates, filepath.Join(dir, command))
			}
		}
	}
	for _, candidate := range candidates {
		variants := []string{candidate}
		if !strings.HasSuffix(strings.ToLower(candidate), ".exe") {
			variants = append(variants, candidate+".exe")
		}
		for _, executable := range variants {
			info, err := os.Stat(executable)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			resolved, err := filepath.EvalSymlinks(executable)
			if err != nil {
				return false, err
			}
			expected := filepath.Join(p.Root, "usr", "bin", base+".exe")
			return strings.EqualFold(filepath.Clean(resolved), filepath.Clean(expected)), nil
		}
	}
	return false, nil // Unknown commands receive the opaque-command policy.
}
