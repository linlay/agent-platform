package coder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadWorkspacePromptUsesExplicitProjectFiles(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("workspace rules"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "RULES.md"), []byte("agent rules"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := LoadWorkspacePrompt(WorkspacePromptPolicy{
		Mode:          Mode,
		AgentDir:      agentDir,
		WorkspaceRoot: workspace,
		ProjectPromptFiles: []ProjectPromptFile{
			{Source: "workspace", Path: "AGENTS.md"},
			{Source: "workspace", Path: "MISSING.md"},
			{Source: "agent", Path: "RULES.md"},
		},
		WorkspaceAgentsEnabled:  true,
		WorkspaceAgentsFileName: "IGNORED.md",
	})
	if err != nil {
		t.Fatalf("LoadWorkspacePrompt: %v", err)
	}
	want := "Workspace AGENTS.md\nworkspace rules\n\nAgent RULES.md\nagent rules"
	if got != want {
		t.Fatalf("prompt=%q want %q", got, want)
	}
}

func TestLoadWorkspacePromptFallbackAndBackendBoundary(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte(" workspace rules \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	policy := WorkspacePromptPolicy{
		Mode:                    Mode,
		WorkspaceRoot:           workspace,
		WorkspaceAgentsEnabled:  true,
		WorkspaceAgentsFileName: "AGENTS.md",
	}
	got, err := LoadWorkspacePrompt(policy)
	if err != nil || got != "workspace rules" {
		t.Fatalf("native fallback prompt=%q err=%v", got, err)
	}
	policy.ACPBridgeID = "codex"
	if got, err := LoadWorkspacePrompt(policy); err != nil || got != "" {
		t.Fatalf("ACP backend must skip local workspace prompts, got %q err=%v", got, err)
	}
	policy.ACPBridgeID = ""
	policy.Mode = "REACT"
	if got, err := LoadWorkspacePrompt(policy); err != nil || got != "" {
		t.Fatalf("non-CODER must skip workspace prompts, got %q err=%v", got, err)
	}
}

func TestLoadWorkspacePromptRejectsTraversal(t *testing.T) {
	_, err := LoadWorkspacePrompt(WorkspacePromptPolicy{
		Mode:                    Mode,
		WorkspaceRoot:           t.TempDir(),
		WorkspaceAgentsEnabled:  true,
		WorkspaceAgentsFileName: "../AGENTS.md",
	})
	if err == nil || !strings.Contains(err.Error(), "invalid workspace AGENTS prompt path") {
		t.Fatalf("expected traversal error, got %v", err)
	}
}

func TestValidateWorkspaceGitSupportsDirectoryAndGitFile(t *testing.T) {
	workspace := t.TempDir()
	gitDir := filepath.Join(workspace, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	policy := WorkspaceGitPolicy{Mode: Mode, WorkspaceRoot: workspace, ExpectedBranch: "main"}
	if err := ValidateWorkspaceGit(policy); err != nil {
		t.Fatalf("directory git metadata: %v", err)
	}

	worktree := t.TempDir()
	metadata := filepath.Join(t.TempDir(), "metadata")
	if err := os.MkdirAll(metadata, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(metadata, "HEAD"), []byte("ref: refs/heads/feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+metadata+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateWorkspaceGit(WorkspaceGitPolicy{Mode: Mode, WorkspaceRoot: worktree, ExpectedBranch: "feature"}); err != nil {
		t.Fatalf("git file metadata: %v", err)
	}
	if err := ValidateWorkspaceGit(WorkspaceGitPolicy{Mode: Mode, WorkspaceRoot: worktree, ExpectedBranch: "main"}); err == nil || !strings.Contains(err.Error(), "workspace git branch mismatch") {
		t.Fatalf("expected branch mismatch, got %v", err)
	}
}
