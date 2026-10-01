package accesspolicy

import (
	"strings"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/pathutil"
)

// BuildSubtreePlan checks protected descendants as well as the target itself.
func BuildSubtreePlan(cfg config.AccessPolicyConfig, session QuerySession, mode AccessMode, raw string) PathPlan {
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
			roots = append(roots, resolveAgainstCwd(root, SessionWorkspaceRoot(session)))
		}
		roots = append(roots, session.SharedConnectorsRoot)
	}
	if chats := SessionChatsRoot(session); chats != "" {
		roots = append(roots, chats)
	}
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
		if pathutil.WithinRoot(root, target) {
			p.Decision = DecisionBlock
			p.Reason = "recursive operation contains a protected subtree: " + root.Host
			if mode == ReadAccess {
				p.Reason += "; use file_grep or file_glob, which skip protected directories"
			}
			return p
		}
	}
	return p
}
