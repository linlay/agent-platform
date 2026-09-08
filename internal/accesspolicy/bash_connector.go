package accesspolicy

import (
	"path"
	"sort"
	"strings"

	"agent-platform/internal/bashast"
	"agent-platform/internal/bashsec"
	"agent-platform/internal/connector"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/pathutil"
)

// connectorExecution checks the actual target, including interpreter script
// operands, against entries frozen at catalog publication. PATH membership alone
// is never an execution grant. Container identity comes only from its resolver.
func connectorExecution(session QuerySession, x BashExecution, vars map[string]string, env *BashEnvironment) bool {
	if len(session.ConnectorCLIEntries) == 0 || x.Uncertain || x.BlockReason != "" || len(x.Argv) == 0 {
		return false
	}
	target := x.Program
	if x.TrustedInterpreter {
		target = x.Script
		if target == "" {
			return false
		}
	} else if target == "" {
		if shellBuiltins[commandFamily(x.Argv[0])] {
			return false
		}
		if session.AgentHasRuntimeSandbox {
			if env == nil || env.Resolve == nil {
				return false
			}
			var err error
			target, err = env.Resolve(x.Argv[0], x.Cwd, vars)
			if err != nil {
				return false
			}
		} else {
			target = resolvedProgram(x.Argv[0], x.Cwd, vars)
		}
	}
	if target == "" {
		return false
	}
	if session.AgentHasRuntimeSandbox {
		if env == nil || env.Canonical == nil || env.Inspect == nil {
			return false
		}
		canonical, err := env.Canonical(target, x.Cwd)
		if err != nil {
			return false
		}
		for _, entry := range session.ConnectorCLIEntries {
			if session.ConnectorDirs[entry.ConnectorID] == "" {
				continue
			}
			expected := path.Join("/connectors", entry.ConnectorID, entry.RelativePath)
			if canonical != expected {
				continue
			}
			_, hash, err := env.Inspect(canonical)
			return err == nil && hash == entry.SHA256
		}
		return false
	}
	canonical, err := NormalizePath(resolveAgainstCwd(target, x.Cwd))
	if err != nil {
		return false
	}
	for _, entry := range session.ConnectorCLIEntries {
		root := session.ConnectorDirs[entry.ConnectorID]
		r, rootErr := pathutil.Canonicalize(root)
		p, pathErr := pathutil.Canonicalize(canonical)
		if root == "" || rootErr != nil || pathErr != nil || !pathutil.WithinRoot(p, r) {
			continue
		}
		if p.Key != entry.PathKey {
			continue
		}
		hash, err := connector.CLIFileHash(canonical)
		return err == nil && hash == entry.SHA256
	}
	return false
}

// connectorReviewCommand removes only verified CLI words from analysis. The
// original source is always executed; redirects, assignments, operators and
// nested shell commands remain subject to ordinary review.
func connectorReviewCommand(command string, words []bashast.WordSpan) string {
	sort.Slice(words, func(i, j int) bool { return words[i].Start < words[j].Start })
	var out strings.Builder
	end := 0
	for _, word := range words {
		if word.Expansion || word.Start < end || word.End > len(command) || word.Start >= word.End {
			continue
		}
		out.WriteString(command[end:word.Start])
		out.WriteString(":")
		end = word.End
	}
	out.WriteString(command[end:])
	return out.String()
}

// SecurityReview binds any residual shell approval to the original command,
// never to the neutral projection used to exclude mounted CLI payloads.
func (p BashPlan) SecurityReview(command string, variables map[string]string) bashsec.ReviewResult {
	result := bashsec.ReviewBashSecurityWithKnownVariables(p.ShellReviewCommand(command), variables)
	if result.Fingerprint != "" {
		result.Fingerprint = bashsec.ApprovalFingerprint(command)
	}
	return result
}

func (p BashPlan) ShellReviewCommand(command string) string {
	if p.HasConnector {
		return p.ReviewCommand
	}
	return command
}
