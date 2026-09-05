package catalog

import (
	"path/filepath"
	"strings"
)

func loadAgentsWithAdmin(root, centerDir, chatsDir string, globalMemoryEnabled bool) (map[string]AgentDefinition, map[string]AdminAgent, error) {
	ruAgentsDir := filepath.Join(filepath.Dir(filepath.Clean(root)), "ru-agents")
	assembler, err := newRuntimeAgentAssembler(ruAgentsDir, centerDir)
	if err != nil {
		return nil, nil, err
	}
	return loadAgentsWithAdminAssembler(root, centerDir, chatsDir, globalMemoryEnabled, assembler)
}

func runtimeSandboxSummaryMeta(runtime map[string]any) map[string]any {
	out := map[string]any{
		"environmentId": strings.TrimSpace(stringNode(runtime["environmentId"])),
		"level":         strings.ToUpper(strings.TrimSpace(stringNode(runtime["level"]))),
	}
	if mounts := listMaps(runtime["sandboxMounts"]); len(mounts) > 0 {
		out["sandboxMounts"] = cloneListMaps(mounts)
	}
	return out
}
