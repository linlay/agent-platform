package tools

import (
	"path/filepath"
	"strings"

	"agent-platform/internal/accesspolicy"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/pathutil"
)

// These exclusions follow user globs, so a positive include cannot reopen a
// protected tree. Callers run rg from searchRoot to anchor the relative globs.
func protectedSearchGlobs(session QuerySession, searchRoot string) ([]string, error) {
	root, err := pathutil.Canonicalize(searchRoot)
	if err != nil {
		return nil, err
	}
	paths := append([]string(nil), session.ProtectedPaths...)
	paths = append(paths, accesspolicy.SessionChatsRoot(session))
	var args []string
	for _, raw := range paths {
		if raw == "" {
			continue
		}
		p, err := pathutil.Canonicalize(raw)
		if err != nil {
			return nil, err
		}
		if !pathutil.WithinRoot(p, root) {
			continue
		}
		rel, err := filepath.Rel(root.Host, p.Host)
		if err != nil {
			return nil, err
		}
		// Access to a protected root itself was already rejected by PathPlan.
		// The ChatsRoot is excluded when traversing its parent, not @chat.
		if rel == "." {
			args = append(args, "--glob", "!**")
			continue
		}
		rel = filepath.ToSlash(rel)
		rel = strings.NewReplacer("\\", "\\\\", "*", "\\*", "?", "\\?", "[", "\\[", "]", "\\]", "{", "\\{", "}", "\\}").Replace(rel)
		args = append(args, "--glob", "!/"+rel, "--glob", "!/"+rel+"/**")
	}
	return args, nil
}
