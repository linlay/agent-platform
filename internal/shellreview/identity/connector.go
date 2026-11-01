package identity

import (
	"path"

	. "agent-platform/internal/accesspolicy"
	"agent-platform/internal/connector"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/pathutil"
	"agent-platform/internal/shellreview/registry"
	"agent-platform/internal/shellreview/types"
)

// connectorExecution checks the actual target, including interpreter script
// operands, against entries frozen at catalog publication. PATH membership alone
// is never an execution grant. Container identity comes only from its resolver.
// connectorExecution returns the ID of the mounted connector whose frozen
// entry the command executes, or "".
func ConnectorExecution(session QuerySession, x types.BashExecution, vars map[string]string, env *types.BashEnvironment) string {
	if len(session.ConnectorCLIEntries) == 0 || x.Uncertain || x.BlockReason != "" || len(x.Argv) == 0 {
		return ""
	}
	target := x.Program
	if x.TrustedInterpreter {
		target = x.Script
		if target == "" {
			return ""
		}
	} else if target == "" {
		if registry.Is(CommandFamily(x.Argv[0]), registry.Builtin) {
			return ""
		}
		if session.AgentHasRuntimeSandbox {
			if env == nil || env.Resolve == nil {
				return ""
			}
			var err error
			target, err = env.Resolve(x.Argv[0], x.Cwd, vars)
			if err != nil {
				return ""
			}
		} else {
			target = ResolvedProgram(x.Argv[0], x.Cwd, vars)
		}
	}
	if target == "" {
		return ""
	}
	if session.AgentHasRuntimeSandbox {
		if env == nil || env.Canonical == nil || env.Inspect == nil {
			return ""
		}
		canonical, err := env.Canonical(target, x.Cwd)
		if err != nil {
			return ""
		}
		for _, entry := range session.ConnectorCLIEntries {
			// Installed programs are host paths; containers only see packages.
			if entry.Root != "" || session.ConnectorDirs[entry.ConnectorID] == "" {
				continue
			}
			expected := path.Join("/connectors", entry.ConnectorID, entry.RelativePath)
			if canonical != expected {
				continue
			}
			if _, hash, err := env.Inspect(canonical); err == nil && hash == entry.SHA256 {
				return entry.ConnectorID
			}
			return ""
		}
		return ""
	}
	canonical, err := NormalizePath(ResolveAgainstCwd(target, x.Cwd))
	if err != nil {
		return ""
	}
	for _, entry := range session.ConnectorCLIEntries {
		// An installed entry is valid only while its connector stays mounted.
		root := session.ConnectorDirs[entry.ConnectorID]
		if root != "" && entry.Root != "" {
			root = entry.Root
		}
		r, rootErr := pathutil.Canonicalize(root)
		p, pathErr := pathutil.Canonicalize(canonical)
		if root == "" || rootErr != nil || pathErr != nil || !pathutil.WithinRoot(p, r) {
			continue
		}
		if p.Key != entry.PathKey {
			continue
		}
		hash, err := connector.CLIFileHash(canonical)
		if err != nil || hash != entry.SHA256 {
			return ""
		}
		if entry.LauncherTarget != "" {
			// The launcher text alone is insufficient when Windows actually
			// executes a different file through the Node argv adapter.
			target, err := connector.WindowsNodeShimTarget(canonical)
			if err != nil {
				return ""
			}
			script, err := pathutil.Canonicalize(target)
			if err != nil || script.Key != entry.LauncherTargetKey || !pathutil.WithinRoot(script, r) {
				return ""
			}
			if hash, err := connector.CLIFileHash(script.Host); err != nil || hash != entry.LauncherTargetSHA256 {
				return ""
			}
		}
		return entry.ConnectorID
	}
	return ""
}

// VerifiedConnectorInvocation resolves the actual child after the root coordinator
// has established one plain call and analyzed its executable identity.
func VerifiedConnectorInvocation(session QuerySession, x types.BashExecution, cwd string, vars map[string]string) *types.ConnectorInvocation {
	if !x.Connector || x.Wrapped || len(x.Argv) == 0 {
		return nil
	}
	var err error
	target := x.Program
	if x.TrustedInterpreter {
		target = x.Script
	}
	if target == "" {
		target = ResolvedProgram(x.Argv[0], cwd, vars)
	}
	target, err = NormalizePath(ResolveAgainstCwd(target, cwd))
	if err != nil {
		return nil
	}
	program := ResolvedProgram(x.Argv[0], cwd, vars)
	if program == "" {
		return nil
	}
	for _, entry := range session.ConnectorCLIEntries {
		if same, err := NormalizePath(entry.Path); err == nil && same == target {
			return &types.ConnectorInvocation{ID: entry.ConnectorID, Program: program, Args: append([]string(nil), x.Argv[1:]...)}
		}
	}
	return nil
}

// VerifiedConnectorCall describes the argument boundary of a verified plain call.
// The coordinator has already excluded shell operators, wrappers and uncertainty.
func VerifiedConnectorCall(x types.BashExecution) *types.ConnectorCall {
	split := 1
	if x.TrustedInterpreter {
		// x.Script may be canonicalized; find the script word as written.
		script := InterpreterScript(CommandFamily(x.Argv[0]), x.Argv[1:])
		split = 0
		for i, token := range x.Argv[1:] {
			if script != "" && token == script {
				split = i + 2
				break
			}
		}
		if split == 0 {
			return nil
		}
	}
	return &types.ConnectorCall{ConnectorID: x.ConnectorID, Prefix: append([]string{}, x.Argv[:split]...), Argv: append([]string{}, x.Argv[split:]...)}
}
