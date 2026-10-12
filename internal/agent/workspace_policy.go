package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ProjectPromptFile is the mode-neutral view of a configured project prompt.
// Catalog adapters should copy only these fields into the policy input.
type ProjectPromptFile struct {
	Source string
	Path   string
}

type WorkspacePromptPolicy struct {
	AgentDir      string
	WorkspaceRoot string
	// ProjectDir is the public project directory; it is empty for chat-type
	// agents (no Workspace or @root), which have no project rules file.
	ProjectDir              string
	ProjectPromptFiles      []ProjectPromptFile
	WorkspaceAgentsEnabled  bool
	WorkspaceAgentsFileName string
}

// LoadWorkspacePrompt resolves project instructions for any native agent.
// Explicit project prompt files take precedence over the project rules file,
// whose switch and file name are configuration defaults of the agent's mode.
func LoadWorkspacePrompt(policy WorkspacePromptPolicy) (string, error) {
	if len(policy.ProjectPromptFiles) > 0 {
		return loadConfiguredProjectPrompts(policy)
	}
	if !policy.WorkspaceAgentsEnabled || strings.TrimSpace(policy.ProjectDir) == "" {
		return "", nil
	}
	workspaceRoot := strings.TrimSpace(policy.WorkspaceRoot)
	if workspaceRoot == "" {
		return "", nil
	}
	fileName := strings.TrimSpace(policy.WorkspaceAgentsFileName)
	if fileName == "" {
		return "", fmt.Errorf("workspace agents file is empty")
	}
	if filepath.IsAbs(fileName) {
		fileName = filepath.Base(fileName)
	}
	cleanFileName, err := cleanRelativePromptPath(fileName)
	if err != nil {
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

func loadConfiguredProjectPrompts(policy WorkspacePromptPolicy) (string, error) {
	workspaceRoot := strings.TrimSpace(policy.WorkspaceRoot)
	sections := make([]string, 0, len(policy.ProjectPromptFiles))
	for _, promptFile := range policy.ProjectPromptFiles {
		source, displayPath, fullPath, err := resolveProjectPromptPath(policy.AgentDir, workspaceRoot, promptFile)
		if err != nil {
			return "", err
		}
		if fullPath == "" {
			continue
		}
		data, err := os.ReadFile(fullPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("read %s project prompt %s: %w", source, fullPath, err)
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			continue
		}
		title := "Workspace " + displayPath
		if source == "agent" {
			title = "Agent " + displayPath
		}
		sections = append(sections, title+"\n"+content)
	}
	return strings.Join(sections, "\n\n"), nil
}

func resolveProjectPromptPath(agentDir string, workspaceRoot string, promptFile ProjectPromptFile) (string, string, string, error) {
	rawPath := strings.TrimSpace(promptFile.Path)
	if rawPath == "" {
		return "", "", "", nil
	}
	source := strings.ToLower(strings.TrimSpace(promptFile.Source))
	if source == "" {
		source = "workspace"
	}
	root := workspaceRoot
	if source == "agent" {
		root = strings.TrimSpace(agentDir)
	} else if source != "workspace" {
		return "", "", "", fmt.Errorf("unsupported project prompt source %q for %q", promptFile.Source, rawPath)
	}
	if root == "" {
		return "", "", "", fmt.Errorf("%s project prompt root is empty for %q", source, rawPath)
	}
	cleanPath, err := cleanRelativePromptPath(rawPath)
	if err != nil {
		return "", "", "", fmt.Errorf("invalid %s project prompt path %q", source, rawPath)
	}
	return source, filepath.ToSlash(cleanPath), filepath.Join(root, cleanPath), nil
}

func cleanRelativePromptPath(path string) (string, error) {
	if filepath.IsAbs(path) {
		path = filepath.Base(path)
	}
	cleanPath := filepath.Clean(path)
	if cleanPath == "." || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid relative path")
	}
	return cleanPath, nil
}

type WorkspaceGitPolicy struct {
	WorkspaceRoot  string
	ExpectedBranch string
}

// ValidateWorkspaceGit enforces an explicitly configured expectedBranch for
// every mode; without the setting there is nothing to check.
func ValidateWorkspaceGit(policy WorkspaceGitPolicy) error {
	expectedBranch := strings.TrimSpace(policy.ExpectedBranch)
	if expectedBranch == "" {
		return nil
	}
	workspaceRoot := strings.TrimSpace(policy.WorkspaceRoot)
	if workspaceRoot == "" {
		return fmt.Errorf("runtimeConfig.workspaceRoot is required when projectConfig.git.expectedBranch is set")
	}
	currentBranch, err := readGitCurrentBranch(workspaceRoot)
	if err != nil {
		return fmt.Errorf("validate workspace git branch for %s: %w", workspaceRoot, err)
	}
	if currentBranch != expectedBranch {
		return fmt.Errorf("workspace git branch mismatch for %s: current %q, expected %q", workspaceRoot, currentBranch, expectedBranch)
	}
	return nil
}

func readGitCurrentBranch(workspaceRoot string) (string, error) {
	gitDirPath := filepath.Join(workspaceRoot, ".git")
	info, err := os.Stat(gitDirPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("not a git repository")
		}
		return "", err
	}
	if !info.IsDir() {
		data, err := os.ReadFile(gitDirPath)
		if err != nil {
			return "", err
		}
		line := strings.TrimSpace(string(data))
		const gitdirPrefix = "gitdir:"
		if !strings.HasPrefix(line, gitdirPrefix) {
			return "", fmt.Errorf("unsupported .git file")
		}
		target := strings.TrimSpace(strings.TrimPrefix(line, gitdirPrefix))
		if !filepath.IsAbs(target) {
			target = filepath.Join(workspaceRoot, target)
		}
		gitDirPath = filepath.Clean(target)
	}
	headPath := filepath.Join(gitDirPath, "HEAD")
	data, err := os.ReadFile(headPath)
	if err != nil {
		return "", err
	}
	head := strings.TrimSpace(string(data))
	const refPrefix = "ref: refs/heads/"
	if strings.HasPrefix(head, refPrefix) {
		return strings.TrimSpace(strings.TrimPrefix(head, refPrefix)), nil
	}
	if head == "" {
		return "", fmt.Errorf("empty git HEAD")
	}
	return "", fmt.Errorf("detached HEAD %q", head)
}
