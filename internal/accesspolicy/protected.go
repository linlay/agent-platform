package accesspolicy

import (
	"strings"

	"agent-platform/internal/config"
)

// PlatformProtectedPaths is assembled from deployment configuration, never
// from model arguments or a public query field. It covers the platform's own
// secrets: the effective StateDir (connector credentials, run control state and
// the default identity file) and a separately configured identity file.
func PlatformProtectedPaths(cfg config.Config) []string {
	state := cfg.Paths.EffectiveStateDir()
	identity := cfg.IdentityFile
	if identity == "" && state != "" {
		identity, _ = config.ResolveIdentityFile(state, "")
	}
	var paths []string
	for _, p := range []string{state, identity} {
		if strings.TrimSpace(p) != "" {
			paths = append(paths, p)
		}
	}
	return paths
}
