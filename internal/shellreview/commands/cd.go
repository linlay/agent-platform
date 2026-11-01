package commands

import (
	"agent-platform/internal/shellreview/registry"
	"strings"
)

// CwdChange describes the existing conservative compound-command cwd tracking.
// Unresolved argument provenance comes from the syntax layer.
func CwdChange(base string, argv []string, commandCount, trackedCount int, unresolved bool, cdpath string) (target string, changed bool, unknown bool) {
	if len(argv) == 0 || registry.IDOf(base) != registry.CmdCd || commandCount <= 1 {
		return "", false, false
	}
	if len(argv) == 2 && !strings.HasPrefix(argv[1], "-") && !unresolved && trackedCount < 8 && cdpath == "" {
		return argv[1], true, false
	}
	return "", false, true
}
