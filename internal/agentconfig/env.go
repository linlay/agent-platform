// Package agentconfig defines the platform-owned environment contract for
// agent and chat execution contexts.
package agentconfig

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// EnvAgentConfigHome is the shared root for agent-owned static tool
	// configuration. Tools append their own name below this directory.
	EnvAgentConfigHome = "AP_AGENT_CONFIG_HOME"
	// EnvWorkspaceDir is the current agent's project workspace.
	EnvWorkspaceDir = "AP_WORKSPACE_DIR"
	// EnvChatDir is the current chat's writable runtime directory. Tools keep
	// chat-scoped state below this directory.
	EnvChatDir = "AP_CHAT_DIR"
)

// HostEnvironment returns the platform-owned environment for a host process.
// Absent paths are intentionally omitted so non-chat administrative callers
// can continue to run without a fabricated execution context.
func HostEnvironment(agentDir string, workspaceDir string, chatDir string) map[string]string {
	agentDir = strings.TrimSpace(agentDir)
	configHome := ""
	if agentDir != "" {
		configHome = filepath.Join(filepath.Clean(agentDir), ".config")
	}
	workspaceDir = strings.TrimSpace(workspaceDir)
	if workspaceDir != "" {
		workspaceDir = filepath.Clean(workspaceDir)
	}
	chatDir = strings.TrimSpace(chatDir)
	if chatDir != "" {
		chatDir = filepath.Clean(chatDir)
	}
	return environment(configHome, workspaceDir, chatDir)
}

// ContainerEnvironment is the equivalent for a Linux container path. It must
// not use filepath.Join because the platform process may run on Windows.
func ContainerEnvironment(agentDir string, workspaceDir string, chatDir string) map[string]string {
	agentDir = strings.TrimSpace(agentDir)
	configHome := ""
	if agentDir != "" {
		configHome = path.Join(agentDir, ".config")
	}
	workspaceDir = strings.TrimSpace(workspaceDir)
	if workspaceDir != "" {
		workspaceDir = path.Clean(workspaceDir)
	}
	chatDir = strings.TrimSpace(chatDir)
	if chatDir != "" {
		chatDir = path.Clean(chatDir)
	}
	return environment(configHome, workspaceDir, chatDir)
}

func environment(configHome string, workspaceDir string, chatDir string) map[string]string {
	var values map[string]string
	if configHome != "" {
		values = map[string]string{EnvAgentConfigHome: configHome}
	}
	if workspaceDir != "" && workspaceDir != "." {
		if values == nil {
			values = make(map[string]string, 3)
		}
		values[EnvWorkspaceDir] = workspaceDir
	}
	if chatDir != "" && chatDir != "." {
		if values == nil {
			values = make(map[string]string, 2)
		}
		values[EnvChatDir] = chatDir
	}
	return values
}

// IsReserved reports whether a key is owned by Agent Platform. The comparison
// is case-insensitive so definitions remain portable to Windows environments.
func IsReserved(key string) bool {
	switch {
	case strings.EqualFold(strings.TrimSpace(key), EnvAgentConfigHome):
		return true
	case strings.EqualFold(strings.TrimSpace(key), EnvWorkspaceDir):
		return true
	case strings.EqualFold(strings.TrimSpace(key), EnvChatDir):
		return true
	default:
		return false
	}
}

// ValidateUserEnvironment rejects agent, skill, or invocation environment
// values that attempt to replace platform-owned execution context.
func ValidateUserEnvironment(values map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if IsReserved(key) {
			return fmt.Errorf("environment variable %q is reserved by Agent Platform", key)
		}
	}
	return nil
}

// Merge applies maps from left to right. Callers must append HostEnvironment
// or ContainerEnvironment last so platform-owned values cannot be replaced.
func Merge(values ...map[string]string) map[string]string {
	var merged map[string]string
	for _, values := range values {
		for key, value := range values {
			if merged == nil {
				merged = make(map[string]string)
			}
			merged[key] = value
		}
	}
	return merged
}
