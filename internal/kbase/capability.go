package kbase

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Requirement string

const (
	RequirementOptional Requirement = "optional"
	RequirementRequired Requirement = "required"
)

type SourceConfig struct {
	Root string
}

// AgentSpec is the fully resolved, mode-neutral KBASE capability owned by one
// catalog agent. The KBASE runtime deliberately has no dependency on agent
// mode; the app adapter resolves mode policy before producing this value.
type AgentSpec struct {
	Key         string
	Enabled     bool
	SourceRoot  string
	Requirement Requirement
	Config      AgentConfig
}

func ResolveSourceRoot(raw string, agentDir string) (string, error) {
	root := strings.TrimSpace(raw)
	if root == "" {
		return "", fmt.Errorf("kbaseConfig.source.root is required when KBASE is enabled")
	}
	if strings.EqualFold(root, WorkspaceRootChat) {
		return "", fmt.Errorf("kbaseConfig.source.root must not be %q", WorkspaceRootChat)
	}
	if root == "~" || strings.HasPrefix(root, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve kbaseConfig.source.root home directory: %w", err)
		}
		if strings.TrimSpace(home) == "" {
			return "", fmt.Errorf("resolve kbaseConfig.source.root home directory: empty home directory")
		}
		if root == "~" {
			root = home
		} else {
			root = filepath.Join(home, strings.TrimPrefix(root, "~/"))
		}
	}
	if !filepath.IsAbs(root) {
		if strings.TrimSpace(agentDir) == "" {
			return "", fmt.Errorf("relative kbaseConfig.source.root is only supported for directory agents")
		}
		root = filepath.Join(agentDir, root)
	}
	root = filepath.Clean(root)
	if isFilesystemRoot(root) {
		return "", fmt.Errorf("kbaseConfig.source.root must not be a filesystem root")
	}
	return root, nil
}

func isFilesystemRoot(path string) bool {
	clean := filepath.Clean(strings.TrimSpace(path))
	if clean == "" || clean == "." {
		return false
	}
	volume := filepath.VolumeName(clean)
	rest := strings.TrimPrefix(clean, volume)
	return rest == string(filepath.Separator)
}
