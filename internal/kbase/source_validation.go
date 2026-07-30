package kbase

import (
	"fmt"
	"path/filepath"
	"strings"

	"agent-platform/internal/pathutil"
	"agent-platform/internal/rootpaths"
)

// ValidateWorkspaceChatsSeparation rejects a KBASE workspace that is the
// filesystem root, equals, contains, or is contained by the runtime chats root. Both paths are
// canonicalized so symlink overlap is treated as real overlap.
func ValidateWorkspaceChatsSeparation(workspaceRoot string, chatsRoot string) error {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	chatsRoot = strings.TrimSpace(chatsRoot)
	if workspaceRoot == "" {
		return fmt.Errorf("runtimeConfig.workspaceRoot is required when KBASE is enabled")
	}
	workspace, err := pathutil.Canonicalize(workspaceRoot)
	if err != nil {
		return fmt.Errorf("resolve KBASE workspace root: %w", err)
	}
	if IsFilesystemRoot(workspace.Host) {
		return fmt.Errorf("KBASE workspace root must not be a filesystem root")
	}
	if chatsRoot == "" {
		return nil
	}
	chats, err := pathutil.Canonicalize(chatsRoot)
	if err != nil {
		return fmt.Errorf("resolve runtime chats root: %w", err)
	}
	if err := rootpaths.ValidateWorkspaceChats(workspace, chats); err != nil ||
		pathutil.WithinRoot(chats, workspace) {
		return fmt.Errorf("KBASE workspace root must be separate from the runtime chats root")
	}
	return nil
}

func IsFilesystemRoot(rawPath string) bool {
	clean := filepath.Clean(strings.TrimSpace(rawPath))
	if clean == "" || clean == "." {
		return false
	}
	volume := filepath.VolumeName(clean)
	rest := strings.TrimPrefix(clean, volume)
	return rest == string(filepath.Separator)
}
