package identity

import (
	"maps"
	"os/exec"
	"path/filepath"
	"strings"

	"agent-platform/internal/builtins"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/pathutil"
	"agent-platform/internal/shellreview/commands"
	"agent-platform/internal/shellreview/registry"
	"agent-platform/internal/shellreview/types"
)

var systemExecutables = captureSystemExecutables()

func CommandFamily(name string) string {
	return strings.TrimSuffix(strings.ToLower(filepath.Base(strings.ReplaceAll(name, "\\", "/"))), ".exe")
}

func captureSystemExecutables() map[string]string {
	out := map[string]string{}
	names := append(registry.Names(registry.Capture), commands.Programs()...)
	for _, name := range names {
		if p, err := exec.LookPath(name); err == nil {
			if c, err := pathutil.Canonicalize(p); err == nil {
				out[CommandFamily(name)] = c.Key
			}
		}
	}
	return out
}

func ResolveIdentity(session QuerySession, name, cwd string, vars map[string]string, env *types.BashEnvironment, allowBuiltins bool) types.CommandIdentity {
	base := CommandFamily(name)
	bare := !strings.ContainsAny(name, "/\\")
	if allowBuiltins && bare && registry.Is(base, registry.Builtin|registry.ShellWrapper) {
		return types.CommandIdentity{Domain: types.ShellBuiltin, Source: types.BuiltinProgram}
	}
	if session.AgentHasRuntimeSandbox {
		if env == nil || env.Resolve == nil {
			return types.CommandIdentity{}
		}
		p, err := env.Resolve(name, cwd, vars)
		if err != nil {
			return types.CommandIdentity{}
		}
		// Resolve the same command's system installation in the container. Merely
		// landing under /usr/bin is insufficient: ./ls could point to python.
		// Canonical comparison still permits sh -> dash and versioned Python.
		for _, root := range []string{"/usr/bin/", "/bin/", "/usr/local/bin/"} {
			if system, err := env.Resolve(root+base, cwd, vars); err == nil && system == p {
				return types.CommandIdentity{Domain: types.NativeCLI, Source: types.ContainerSystemProgram, Path: p}
			}
		}
		return types.CommandIdentity{}
	}
	p := ResolvedProgram(name, cwd, vars)
	if p == "" {
		return types.CommandIdentity{}
	}
	canonical, err := pathutil.Canonicalize(p)
	if err != nil {
		return types.CommandIdentity{}
	}
	systems := systemExecutables
	if env != nil && env.SystemPrograms != nil {
		systems = env.SystemPrograms
	}
	if systems[base] == canonical.Key {
		return types.CommandIdentity{Domain: types.NativeCLI, Source: types.SystemProgram, Path: canonical.Host}
	}
	if registry.Is(base, registry.PlatformBuiltin) {
		if builtin, err := builtins.ResolveProcessBuiltin(base); err == nil {
			if c, err := pathutil.Canonicalize(builtin); err == nil && c.Key == canonical.Key {
				return types.CommandIdentity{Domain: types.NativeCLI, Source: types.PlatformProgram, Path: c.Host}
			}
		}
	}
	return types.CommandIdentity{}
}

func IsOrdinaryCommand(base string) bool {
	return registry.Is(base, registry.Arguments|registry.Builtin) || commands.IsProgram(base)
}
func IsOpaqueCommand(base string) bool {
	return registry.Is(CommandFamily(base), registry.Interpreter|registry.BuildTool)
}

// SystemPrograms returns a copy of the system installations captured at startup.
// A caller can freeze a separate snapshot without changing production trust.
func SystemPrograms() map[string]string { return maps.Clone(systemExecutables) }

// OrdinaryCommands returns the current recognized-program inventory.
func OrdinaryCommands() []string {
	return append(registry.Names(registry.Arguments), commands.Programs()...)
}

// ShellBuiltins returns a copy of the current builtin inventory.
func ShellBuiltins() map[string]bool { return inventory(registry.Builtin) }

// WrapperNames returns a copy of the current wrapper inventory.
func WrapperNames() map[string]bool { return inventory(registry.Wrapper) }

// IsWrapper reports whether a command family has a modeled wrapper grammar.
func IsWrapper(base string) bool { return registry.Is(base, registry.Wrapper) }

func inventory(flag registry.Flag) map[string]bool {
	out := map[string]bool{}
	for _, name := range registry.Names(flag) {
		out[strings.ToLower(name)] = true
	}
	return out
}
