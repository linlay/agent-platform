package accesspolicy

import (
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/contracts"
	"agent-platform/internal/pathutil"
)

// Connector secrets are a platform boundary, not an outside-workspace approval.
// Parent directory scans are denied too, so recursive grep/glob cannot include a
// private descendant. Shared versioned installations contain no user credentials.
func connectorPrivatePath(session contracts.QuerySession, candidate pathutil.Canonical) (pathutil.Canonical, bool) {
	root := strings.TrimSpace(session.ConnectorStateRoot)
	if root == "" {
		return pathutil.Canonical{}, false
	}
	private, err := pathutil.Canonicalize(root)
	if err != nil {
		return pathutil.Canonical{}, true
	}
	if pathutil.WithinRoot(candidate, private) {
		install, err := pathutil.Canonicalize(filepath.Join(root, "installations"))
		info, statErr := os.Lstat(filepath.Join(root, "installations"))
		if err == nil && statErr == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && pathutil.WithinRoot(install, private) && pathutil.WithinRoot(candidate, install) {
			return pathutil.Canonical{}, false
		}
		return private, true
	}
	if pathutil.WithinRoot(private, candidate) {
		return private, true
	}
	return pathutil.Canonical{}, false
}
