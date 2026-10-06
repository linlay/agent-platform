package catalog

import (
	"fmt"
	"strings"

	"agent-platform/internal/pathutil"
)

// ValidateProjectWorkspace checks a caller's project intent without persisting
// it. Ordinary Agent definitions may still use @root or a filesystem root.
func ValidateProjectWorkspace(definition map[string]any) error {
	workspace := parseAgentWorkspaceRoot(mapNode(definition["runtimeConfig"])["workspaceRoot"])
	if strings.TrimSpace(workspace.Root) == "" || workspace.HostRoot {
		return fmt.Errorf("isProject:true requires runtimeConfig.workspaceRoot to name a specific project directory; empty values and @root are not project directories")
	}
	if err := validateAgentWorkspace(workspace); err != nil {
		return err
	}
	canonical, err := pathutil.Canonicalize(workspace.Root)
	if err != nil {
		return err
	}
	if pathutil.IsCanonicalFilesystemRoot(canonical.Host) {
		return fmt.Errorf("isProject:true requires a specific project directory, not a filesystem, volume or share root")
	}
	return nil
}
