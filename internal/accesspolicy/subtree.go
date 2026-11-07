package accesspolicy

import (
	"slices"
	"strings"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/pathutil"
)

// BuildSubtreePlan checks protected descendants as well as the target itself.
func BuildSubtreePlan(cfg config.AccessPolicyConfig, session QuerySession, mode AccessMode, raw string) PathPlan {
	return BuildWalkPlan(cfg, session, mode, raw, nil)
}

// BuildWalkPlan is BuildSubtreePlan for a walk that provably never visits
// some descendants: skip reports a protected root (canonical) the walk of
// target (canonical) passes by.
func BuildWalkPlan(cfg config.AccessPolicyConfig, session QuerySession, mode AccessMode, raw string, skip func(target, root string) bool) PathPlan {
	p, err := BuildPathPlan(cfg, session, mode, raw)
	if err != nil {
		return PathPlan{Decision: DecisionBlock, Reason: err.Error()}
	}
	if p.Blocked() {
		return p
	}
	target, err := pathutil.Canonicalize(p.Path)
	if err != nil {
		return PathPlan{Decision: DecisionBlock, Reason: err.Error()}
	}
	roots := append([]string(nil), session.ProtectedPaths...)
	if mode == WriteAccess {
		level := EffectiveLevel(cfg, session.AccessLevel)
		for _, root := range append(level.ReadonlyRoots, session.RunAccessRoots.ReadonlyRoots...) {
			if strings.HasPrefix(strings.TrimSpace(root), "@") {
				// An unconfigured alias protects nothing; it never falls back to a
				// Workspace-relative directory literally named "@agent".
				if resolved := expandRootAlias(root, session); resolved != "" {
					roots = append(roots, resolved)
				}
				continue
			}
			roots = append(roots, ResolveAgainstCwd(root, SessionWorkspaceRoot(session)))
		}
		roots = append(roots, session.SharedConnectorsRoot)
		roots = append(roots, session.ControlReadonlyRoots...)
	}
	if chats := SessionChatsRoot(session); chats != "" {
		roots = append(roots, chats)
	}
	var hits []string
	for _, rawRoot := range roots {
		if rawRoot == "" {
			continue
		}
		root, err := pathutil.Canonicalize(rawRoot)
		if err != nil {
			p.Decision = DecisionBlock
			p.Reason = "cannot resolve protected subtree"
			return p
		}
		if pathutil.WithinRoot(root, target) && !(skip != nil && skip(target.Host, root.Host)) && !slices.Contains(hits, root.Host) {
			hits = append(hits, root.Host)
		}
	}
	if len(hits) > 0 {
		// Naming every hit lets one retry exclude them all.
		p.Decision = DecisionBlock
		p.Reason = "recursive operation contains a protected subtree: " + strings.Join(hits, ", ")
		if mode == ReadAccess {
			p.Reason += "; run it only on paths outside it (find skips a directory with -path '<dir>' -prune -o ...; a command run by find -exec is checked against find's start path), or use file_grep or file_glob, which skip protected directories"
		}
	}
	return p
}
