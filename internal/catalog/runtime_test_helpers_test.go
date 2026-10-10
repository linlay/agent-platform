package catalog

import (
	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/runtimeskills"
	"path/filepath"
	"strings"
	"testing"
)

func cleanupRuntimeTrees(t *testing.T, root string) {
	t.Helper()
	t.Cleanup(func() {
		if err := runtimeskills.Remove(root); err != nil {
			t.Error(err)
		}
		if err := runtimeskills.Remove(runtimeskills.Root(root)); err != nil {
			t.Error(err)
		}
	})
}
func newVersionTestRegistry(t *testing.T, cfg config.Config, defs []api.ToolDetailResponse) (*FileRegistry, error) {
	t.Helper()
	cleanupRuntimeTrees(t, cfg.Paths.EffectiveRUAgentsDir())
	return NewFileRegistry(cfg, defs)
}
func newVersionTestAssembler(t *testing.T, root, center string, connectors ...string) (*runtimeAgentAssembler, error) {
	t.Helper()
	cleanupRuntimeTrees(t, root)
	return newRuntimeAgentAssembler(root, center, connectors...)
}

func runtimeSkillTestPath(t *testing.T, agent, id string, parts ...string) string {
	t.Helper()
	root, err := runtimeskills.Resolve(agent, id)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(append([]string{root}, parts...)...)
}
func runtimeResourceTestPath(t *testing.T, agent, rel string) string {
	if strings.HasPrefix(rel, "skills/") {
		id, tail, _ := strings.Cut(strings.TrimPrefix(rel, "skills/"), "/")
		return runtimeSkillTestPath(t, agent, id, tail)
	}
	return filepath.Join(agent, filepath.FromSlash(rel))
}
