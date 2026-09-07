// Package hostshell owns the host shell launch contract shared by command tools
// and Workspace Terminal. It does not import tools, server or accesspolicy.
package hostshell

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"agent-platform/internal/builtins"
	"agent-platform/internal/config"
	"agent-platform/internal/shellenv"
)

type Invocation struct {
	Executable string
	Args       []string
	Env        []string
	CWD        string
	GitBash    bool
}

type Options struct {
	GOOS        string
	Command     string
	Interactive bool
	CWD         string
	Env         []string
	TempDir     string
}

func Enabled(cfg config.BashConfig, goos string) bool {
	return goos == "windows" && cfg.GitBash.Enabled
}

func Configure(cfg *config.BashConfig, goos, goarch string) error {
	cfg.GitBash.RuntimeRoot = ""
	if !Enabled(*cfg, goos) {
		return nil
	}
	root, err := builtins.ResolveGitBash(goos, goarch)
	if err != nil {
		return fmt.Errorf("%w; restore the builtin cache or set bash.git-bash.enabled=false and restart", err)
	}
	cfg.GitBash.RuntimeRoot = root
	return nil
}

func Resolve(cfg config.BashConfig, opts Options) (Invocation, error) {
	inv := Invocation{CWD: opts.CWD, Env: shellenv.StripLocator(opts.Env)}
	if !Enabled(cfg, opts.GOOS) {
		if opts.Interactive {
			inv.Executable = TerminalExecutable(cfg.ShellExecutable, envValue(opts.Env, "SHELL"), opts.GOOS)
		} else {
			inv.Executable, inv.Args = LegacyInvocation(cfg, opts.Command, opts.GOOS)
		}
		return inv, nil
	}
	root := cfg.GitBash.RuntimeRoot
	if root == "" || !filepath.IsAbs(root) {
		return Invocation{}, fmt.Errorf("git_bash_unavailable: managed runtime has not been verified at startup")
	}
	inv.GitBash = true
	inv.Executable = filepath.Join(root, "usr", "bin", "bash.exe")
	inv.Args = []string{"--noprofile", "--norc"}
	if opts.Interactive {
		inv.Args = append(inv.Args, "-i")
	} else {
		inv.Args = append(inv.Args, "-o", "pipefail", "-c", opts.Command)
	}
	var err error
	inv.Env, err = managedEnvironment(inv.Env, root, inv.Executable, opts.TempDir)
	return inv, err
}

func managedEnvironment(base []string, root, executable, temp string) ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("git_bash_environment: resolve user home: %w", err)
	}
	if temp == "" {
		return nil, fmt.Errorf("git_bash_environment: frozen temporary root is required")
	}
	values := map[string]string{}
	names := map[string]string{}
	for _, entry := range base {
		key, value, ok := strings.Cut(entry, "=")
		if ok && !shellenv.Reserved(key) {
			canonical := strings.ToUpper(key)
			if previous := names[canonical]; previous != "" {
				delete(values, previous)
			}
			values[key], names[canonical] = value, key
		}
	}
	inheritedPath := values[names["PATH"]]
	for _, key := range []string{"PATH", "HOME", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL"} {
		delete(values, names[key])
	}
	// Supply a native PATH to MSYS. Its special PATH handling performs the
	// boundary conversion; only generic argument/environment guessing is off.
	paths := []string{builtins.ProcessBinDir(), filepath.Join(root, "mingw64", "bin"), filepath.Join(root, "usr", "bin")}
	paths = append(paths, strings.Split(inheritedPath, ";")...)
	seen := map[string]bool{}
	var cleaned []string
	for _, dir := range paths {
		key := strings.ToLower(strings.TrimSpace(dir))
		if key != "" && !seen[key] {
			cleaned = append(cleaned, dir)
			seen[key] = true
		}
	}
	values["PATH"] = strings.Join(cleaned, ";")
	values["HOME"] = home
	values["TMPDIR"], values["TMP"], values["TEMP"] = temp, temp, temp
	values["MSYSTEM"] = "MINGW64"
	values["MSYS2_ARG_CONV_EXCL"], values["MSYS_NO_PATHCONV"] = "*", "1"
	// Keep PATH's dedicated conversion, but protect all other inherited values
	// (including run-local API paths, JSON and the native AP_* path contract).
	var excluded []string
	for key := range values {
		if key != "PATH" {
			excluded = append(excluded, key+"=")
		}
	}
	values[shellenv.GitBashExecutable] = executable
	excluded = append(excluded, shellenv.GitBashExecutable+"=")
	sort.Strings(excluded)
	values["MSYS2_ENV_CONV_EXCL"] = strings.Join(excluded, ";")
	values["LANG"], values["LC_ALL"] = "C.UTF-8", "C.UTF-8"
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+values[key])
	}
	return out, nil
}

func envValue(env []string, name string) string {
	for i := len(env) - 1; i >= 0; i-- {
		key, value, _ := strings.Cut(env[i], "=")
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

func TerminalExecutable(configured, envShell, goos string) string {
	if configured = strings.TrimSpace(configured); configured != "" {
		return configured
	}
	if goos == "windows" {
		return "powershell.exe"
	}
	if envShell = strings.TrimSpace(envShell); envShell != "" {
		return envShell
	}
	return "/bin/bash"
}

// CommandBase recognizes both path separators even in cross-platform tests.
// The executable suffix is significant on Windows, not on Unix hosts.
func CommandBase(command string, windows bool) string {
	if windows {
		command = strings.ReplaceAll(command, `\`, "/")
	}
	base := strings.ToLower(path.Base(strings.TrimSpace(command)))
	if windows {
		base = strings.TrimSuffix(base, ".exe")
	}
	return base
}

func LegacyInvocation(cfg config.BashConfig, command, goos string) (string, []string) {
	executable := strings.TrimSpace(cfg.ShellExecutable)
	if executable == "" {
		executable = "bash"
		if goos == "windows" {
			executable = "powershell.exe"
		}
	}
	var args []string
	for _, arg := range cfg.ShellArgs {
		if arg = strings.TrimSpace(arg); arg != "" {
			args = append(args, arg)
		}
	}
	if len(args) == 0 {
		base := CommandBase(executable, true)
		if goos == "windows" {
			switch base {
			case "powershell", "pwsh":
				args = []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", "{{command}}"}
				command = "$OutputEncoding = New-Object System.Text.UTF8Encoding -ArgumentList $false; [Console]::OutputEncoding = $OutputEncoding; " + command
			case "cmd":
				args = []string{"/d", "/s", "/c", "{{command}}"}
				command = "chcp 65001 >NUL & " + command
			case "bash", "sh":
				args = []string{"-lc", "{{command}}"}
			default:
				args = []string{"{{command}}"}
			}
		} else if base == "bash" {
			args = []string{"-o", "pipefail", "-lc", "{{command}}"}
		} else {
			args = []string{"-lc", "{{command}}"}
		}
	}
	replaced := false
	for i, arg := range args {
		if strings.Contains(arg, "{{command}}") {
			args[i] = strings.ReplaceAll(arg, "{{command}}", command)
			replaced = true
		}
	}
	if !replaced {
		args = append(args, command)
	}
	return executable, args
}
