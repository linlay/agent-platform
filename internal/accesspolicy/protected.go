package accesspolicy

import (
	"strings"

	"agent-platform/internal/config"
)

// PlatformProtectedPaths is assembled from deployment configuration, never
// from model arguments or a public query field. It covers the platform's own
// secrets: the effective StateDir (connector credentials and the default
// identity file), a separately configured identity file, and the document
// preview records, whose share links open the previewed documents.
func PlatformProtectedPaths(cfg config.Config) []string {
	state := cfg.Paths.EffectiveStateDir()
	identity := cfg.IdentityFile
	if identity == "" && state != "" {
		identity, _ = config.ResolveIdentityFile(state, "")
	}
	var paths []string
	for _, p := range []string{state, identity, cfg.Paths.EffectiveRUPreviewsDir()} {
		if strings.TrimSpace(p) != "" {
			paths = append(paths, p)
		}
	}
	return paths
}
