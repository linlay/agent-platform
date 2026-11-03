package accesspolicy

import (
	"path/filepath"
	"strings"

	"agent-platform/internal/pathutil"
)

// ResolveAgainstCwd resolves a relative operand without cleaning away physical traversal.
func ResolveAgainstCwd(raw string, cwd string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || filepath.IsAbs(raw) || strings.HasPrefix(raw, "~") {
		return raw
	}
	if strings.TrimSpace(cwd) == "" {
		return raw
	}
	return pathutil.JoinUnclean(cwd, raw)
}
