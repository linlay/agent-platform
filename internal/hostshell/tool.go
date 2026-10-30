package hostshell

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"agent-platform/internal/config"
	"agent-platform/internal/hostenv"
	"agent-platform/internal/pathutil"
	"agent-platform/internal/shellreview/dialect"
	"agent-platform/internal/shellreview/types"
)

// LaunchSpec is a tool invocation, independent of Terminal/Git Bash settings.
// ResolveTool owns its slices; callers must pass these exact values to exec.
type LaunchSpec struct {
	Invocation
	Binding types.LaunchBinding
}

var toolSystemShells = captureToolSystemShells()

type toolShellIdentity struct{ Path, Content string }

func captureToolSystemShells() map[string]toolShellIdentity {
	out := map[string]toolShellIdentity{}
	env := hostenv.WithSystemPaths(os.Environ())
	for _, name := range []string{"bash", "sh", "dash", "powershell", "pwsh"} {
		if p, err := hostenv.LookPath(name, env); err == nil {
			if p, err = filepath.EvalSymlinks(p); err == nil {
				if content, err := toolFileFingerprint(p); err == nil {
					out[name] = toolShellIdentity{p, content}
				}
			}
		}
	}
	return out
}

// ToolInvocation preserves the Bash tool launch contract. Terminal defaults
// deliberately remain separate: tools never implicitly enable a login shell.
func ToolInvocation(cfg config.BashConfig, command, goos string) (string, []string) {
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
				args = []string{"-c", "{{command}}"}
			default:
				args = []string{"{{command}}"}
			}
		} else if base == "bash" {
			args = []string{"--noprofile", "--norc", "-o", "pipefail", "-c", "{{command}}"}
		} else {
			args = []string{"-c", "{{command}}"}
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

// ResolveTool never runs the configured shell to inspect it. It resolves and
// hashes the executable and freezes the reviewed environment before binding the
// fixed PATH preamble. Unsupported grammars remain facts for access policy.
func ResolveTool(cfg config.BashConfig, opts Options) (LaunchSpec, error) {
	cwd, err := pathutil.Canonicalize(opts.CWD)
	if err != nil {
		return LaunchSpec{}, fmt.Errorf("shell_launch_cwd: %w", err)
	}
	name, _ := ToolInvocation(cfg, "{{command}}", opts.GOOS)
	env := toolEnvironment(opts.Env, opts.GOOS == "windows")
	program, err := hostenv.LookPath(name, env)
	if err != nil {
		return LaunchSpec{}, fmt.Errorf("shell_unavailable: %w", err)
	}
	program, err = filepath.EvalSymlinks(program)
	if err != nil {
		return LaunchSpec{}, fmt.Errorf("shell_unavailable: %w", err)
	}
	content, err := toolFileFingerprint(program)
	if err != nil {
		return LaunchSpec{}, fmt.Errorf("shell_unavailable: %w", err)
	}
	_, template := ToolInvocation(cfg, "{{command}}", opts.GOOS)
	binding := classifyToolShell(name, program, content, template, opts.GOOS)
	binding.Executable = program
	binding.Template = append([]string(nil), template...)
	bound := hostenv.BindShellEnvironment(name, opts.Command, env, nil)
	_, args := ToolInvocation(cfg, bound, opts.GOOS)
	contract := struct {
		Program, Content, CWD, GOOS string
		Template, Env               []string
	}{program, content, cwd.Host, opts.GOOS, template, env}
	binding.ContractFingerprint = toolFingerprint(contract)
	binding.Fingerprint = toolFingerprint(struct {
		Contract string
		Args     []string
	}{binding.ContractFingerprint, args})
	return LaunchSpec{Invocation: Invocation{Executable: program, Args: args, Env: env, CWD: cwd.Host}, Binding: binding}, nil
}

func classifyToolShell(name, canonical, content string, args []string, goos string) types.LaunchBinding {
	base := CommandBase(name, goos == "windows")
	b := types.LaunchBinding{Dialect: dialect.Unknown, Reason: "configured shell grammar or startup template is not supported"}
	if goos == "windows" {
		switch base {
		case "powershell", "pwsh":
			b.Dialect = dialect.PowerShell
		case "cmd":
			b.Dialect = dialect.CMD
		case "bash", "sh":
			b.Dialect = dialect.MSYS
		}
		return b
	}
	if base != "bash" {
		return b
	}
	b.Dialect = dialect.Bash
	if toolSystemShells[base] != (toolShellIdentity{canonical, content}) {
		b.Reason = "configured shell does not match the frozen system installation"
		return b
	}
	// Accept only a literal command slot and understood non-interactive flags.
	// Login, profiles, executable templates and multiple command slots are opaque.
	commandSlot := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--noprofile", "--norc", "-e", "-u", "-eu", "-ue":
		case "-o":
			i++
			if i >= len(args) || args[i] != "pipefail" {
				return b
			}
		case "-c":
			if commandSlot || i+1 != len(args)-1 || args[i+1] != "{{command}}" {
				return b
			}
			commandSlot = true
			i++
		default:
			return b
		}
	}
	b.SyntaxSupported = commandSlot
	if commandSlot {
		b.Reason = ""
	}
	return b
}

func toolFingerprint(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func toolFileFingerprint(program string) (string, error) {
	f, err := os.Open(program)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// Match exec.Cmd's last-value-wins environment semantics before sorting. In
// particular, swapping duplicate PATH entries must change both launch and hash.
func toolEnvironment(env []string, windows bool) []string {
	values := map[string]string{}
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if windows {
			key = strings.ToUpper(key)
		}
		values[key] = entry
	}
	out := make([]string, 0, len(values))
	for _, entry := range values {
		out = append(out, entry)
	}
	sort.Strings(out)
	return out
}
