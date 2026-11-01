package identity

import (
	"maps"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"agent-platform/internal/builtins"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/pathutil"
	"agent-platform/internal/shellreview/types"
)

// ordinaryCommands are recognized system programs whose effects shellreview/commands
// models from their arguments; anything else is opaque execution.
var ordinaryCommands = strings.Fields("ls cat head tail top free df git rg dbx httpx pdftotext find echo printf sed awk gawk grep wc sort uniq tr cut cd stat file du test which mkdir rmdir touch cp mv rm ln chmod date curl wget lsof pwd true false sleep uname whoami id basename dirname readlink realpath tee printenv jq tar diff Get-ChildItem Get-Content Remove-Item")

var shellBuiltins = wordSet("echo printf pwd cd test true false type read export unset declare local typeset set shift break continue return :")

var wrapperNames = wordSet("env command builtin nohup nice timeout stdbuf")

var systemExecutables = captureSystemExecutables()

func wordSet(words string) map[string]bool {
	out := map[string]bool{}
	for _, word := range strings.Fields(words) {
		out[strings.ToLower(word)] = true
	}
	return out
}

func CommandFamily(name string) string {
	return strings.TrimSuffix(strings.ToLower(filepath.Base(strings.ReplaceAll(name, "\\", "/"))), ".exe")
}

func captureSystemExecutables() map[string]string {
	out := map[string]string{}
	names := append(append([]string{}, ordinaryCommands...), strings.Fields("env nohup nice timeout stdbuf bash sh zsh dash ksh python python3 node ruby perl powershell pwsh cmd go npm npx yarn pnpm pip make")...)
	for _, name := range names {
		if p, err := exec.LookPath(name); err == nil {
			if c, err := pathutil.Canonicalize(p); err == nil {
				out[CommandFamily(name)] = c.Key
			}
		}
	}
	return out
}

func RecognizedProgram(session QuerySession, name, cwd string, vars map[string]string, env *types.BashEnvironment, allowBuiltins bool) bool {
	base := CommandFamily(name)
	bare := !strings.ContainsAny(name, "/\\")
	if allowBuiltins && bare && (shellBuiltins[base] || base == "command" || base == "builtin") {
		return true
	}
	if session.AgentHasRuntimeSandbox {
		if env == nil || env.Resolve == nil {
			return false
		}
		p, err := env.Resolve(name, cwd, vars)
		if err != nil {
			return false
		}
		// Resolve the same command's system installation in the container. Merely
		// landing under /usr/bin is insufficient: ./ls could point to python.
		// Canonical comparison still permits sh -> dash and versioned Python.
		for _, root := range []string{"/usr/bin/", "/bin/", "/usr/local/bin/"} {
			if system, err := env.Resolve(root+base, cwd, vars); err == nil && system == p {
				return true
			}
		}
		return false
	}
	p := ResolvedProgram(name, cwd, vars)
	if p == "" {
		return false
	}
	canonical, err := pathutil.Canonicalize(p)
	if err != nil {
		return false
	}
	systems := systemExecutables
	if env != nil && env.SystemPrograms != nil {
		systems = env.SystemPrograms
	}
	if systems[base] == canonical.Key {
		return true
	}
	if base == "rg" || base == "dbx" || base == "httpx" || base == "pdftotext" {
		if builtin, err := builtins.ResolveProcessBuiltin(base); err == nil {
			if c, err := pathutil.Canonicalize(builtin); err == nil && c.Key == canonical.Key {
				return true
			}
		}
	}
	return false
}

func IsOrdinaryCommand(base string) bool {
	if shellBuiltins[base] {
		return true
	}
	for _, s := range ordinaryCommands {
		if strings.EqualFold(s, base) {
			return true
		}
	}
	return false
}

func IsOpaqueCommand(base string) bool {
	base = CommandFamily(base)
	if IsInterpreter(base) {
		return true
	}
	switch base {
	case "go", "npm", "npx", "yarn", "pnpm", "node", "python", "python3", "pip", "make", "bash", "sh":
		return true
	default:
		return false
	}
}

// SystemPrograms returns a copy of the system installations captured at startup.
// A caller can freeze a separate snapshot without changing production trust.
func SystemPrograms() map[string]string { return maps.Clone(systemExecutables) }

// OrdinaryCommands returns the current recognized-program inventory.
func OrdinaryCommands() []string { return slices.Clone(ordinaryCommands) }

// ShellBuiltins returns a copy of the current builtin inventory.
func ShellBuiltins() map[string]bool { return maps.Clone(shellBuiltins) }

// WrapperNames returns a copy of the current wrapper inventory.
func WrapperNames() map[string]bool { return maps.Clone(wrapperNames) }

// IsWrapper reports whether a command family has a modeled wrapper grammar.
func IsWrapper(base string) bool { return wrapperNames[base] }
