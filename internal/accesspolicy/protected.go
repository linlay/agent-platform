package accesspolicy

import (
	"path/filepath"
	"strings"

	"agent-platform/internal/config"
)

// PlatformProtectedPaths is assembled from deployment configuration, never
// from model arguments or a public query field. It covers the platform's own
// secrets: the effective StateDir (connector credentials and the default
// identity file), a separately configured identity file, and the document
// preview records, whose share links open the previewed documents.
//
// Under Desktop the runtime root also holds Desktop's own secrets partition
// (<runtime root>/.desktop/secrets, the saved website passwords). Agents use
// those only through surface_login, never by reading the files.
func PlatformProtectedPaths(cfg config.Config) []string {
	state := cfg.Paths.EffectiveStateDir()
	identity := cfg.SsoIdTokenFile
	if identity == "" && state != "" {
		identity, _ = config.ResolveSsoIdTokenFile(state, "")
	}
	var paths []string
	for _, p := range []string{state, identity, cfg.Paths.EffectiveRUPreviewsDir()} {
		if strings.TrimSpace(p) != "" {
			paths = append(paths, p)
		}
	}
	if cfg.RuntimeMode == config.RuntimeModeDesktop && strings.TrimSpace(state) != "" {
		paths = append(paths, filepath.Join(filepath.Dir(state), ".desktop", "secrets"))
	}
	return paths
}

// PlatformControlReadonlyRoots are readable under the ordinary policy, but
// only dedicated management transactions may mutate their source definitions.
func PlatformControlReadonlyRoots(cfg config.Config) []string {
	var roots []string
	for _, path := range []string{cfg.Paths.AgentsDir, cfg.Paths.EffectiveRUAgentsDir(), cfg.Paths.AutomationsDir} {
		if strings.TrimSpace(path) != "" {
			roots = append(roots, path)
		}
	}
	return roots
}
