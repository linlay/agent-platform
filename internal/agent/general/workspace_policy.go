package general

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type WorkspacePromptPolicy struct {
	Mode string
	// WorkspaceRoot is the resolved project directory. It must be empty for
	// chat-type agents (no Workspace or @root), which never read the file.
	WorkspaceRoot           string
	WorkspaceAgentsEnabled  bool
	WorkspaceAgentsFileName string
}

// LoadWorkspacePrompt reads the project rules file of a project-type general
// agent. It is off unless general-settings.yml enables it.
func LoadWorkspacePrompt(policy WorkspacePromptPolicy) (string, error) {
	if !IsMode(policy.Mode) || !policy.WorkspaceAgentsEnabled {
		return "", nil
	}
	workspaceRoot := strings.TrimSpace(policy.WorkspaceRoot)
	if workspaceRoot == "" {
		return "", nil
	}
	fileName := strings.TrimSpace(policy.WorkspaceAgentsFileName)
	if fileName == "" {
		return "", fmt.Errorf("general workspace agents file is empty")
	}
	if filepath.IsAbs(fileName) {
		fileName = filepath.Base(fileName)
	}
	cleanFileName := filepath.Clean(fileName)
	if cleanFileName == "." || cleanFileName == ".." || strings.HasPrefix(cleanFileName, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid workspace AGENTS prompt path %q", fileName)
	}
	agentsPath := filepath.Join(workspaceRoot, cleanFileName)
	data, err := os.ReadFile(agentsPath)
	if err == nil {
		return strings.TrimSpace(string(data)), nil
	}
	if os.IsNotExist(err) {
		return "", nil
	}
	return "", fmt.Errorf("read workspace AGENTS prompt %s: %w", agentsPath, err)
}
