package accesspolicy

import (
	"path/filepath"
	"strings"

	"agent-platform/internal/config"
)

// PlatformProtectedPaths is assembled from deployment configuration, never
// from model arguments or a public query field.
func PlatformProtectedPaths(cfg config.Config) []string {
	state := cfg.Paths.EffectiveStateDir()
	identity := cfg.IdentityFile
	if identity == "" && state != "" {
		identity, _ = config.ResolveIdentityFile(state, "")
	}
	providers := cfg.Providers.ExternalDir
	if providers == "" && cfg.Paths.RegistriesDir != "" {
		providers = filepath.Join(cfg.Paths.RegistriesDir, "providers")
	}
	var paths []string
	for _, p := range []string{state, identity, providers, cfg.Paths.LegacyConnectorStateDir} {
		if strings.TrimSpace(p) != "" {
			paths = append(paths, p)
		}
	}
	return paths
}
