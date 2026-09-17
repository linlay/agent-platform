package project

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
)

func branchFixture(t *testing.T, commit bool) (Service, string) {
	t.Helper()
	root := t.TempDir()
	gitFixtureCommand(t, root, "init", "-b", "base")
	if commit {
		if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("base\n"), 0600); err != nil {
			t.Fatal(err)
		}
		gitFixtureCommand(t, root, "add", "file.txt")
		gitFixtureCommand(t, root, "-c", "core.hooksPath="+os.DevNull, "commit", "-m", "base")
	}
	return Service{Registry: gitRegistry{def: catalog.AgentDefinition{Key: "demo", Mode: "KBASE", Workspace: catalog.AgentWorkspaceConfig{Root: root}}}}, root
}
func branchRequest(t *testing.T, s Service, operation, branch string) api.ProjectGitBranchRequest {
	t.Helper()
	snapshot, err := s.Git(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision == "" {
		t.Fatalf("missing revision: %+v", snapshot)
	}
	return api.ProjectGitBranchRequest{AgentKey: "demo", Operation: operation, Branch: branch, ExpectedRevision: snapshot.Revision}
}
func TestGitBranchesCreateAndSwitch(t *testing.T) {
	s, root := branchFixture(t, true)
	listed, err := s.GitBranches(context.Background(), "demo")
	if err != nil || !listed.CanChange || strings.Join(listed.Branches, ",") != "base" {
		t.Fatalf("list: %+v %v", listed, err)
	}
	create := branchRequest(t, s, "create", "feature/new")
	result, err := s.ChangeGitBranch(context.Background(), create)
	if err != nil || result.Branch != "feature/new" || result.Revision == create.ExpectedRevision {
		t.Fatalf("create: %+v %v", result, err)
	}
	if _, err := s.ChangeGitBranch(context.Background(), create); err == nil || err.(Error).Code != "revision_conflict" {
		t.Fatalf("stale request: %v", err)
	}
	result, err = s.ChangeGitBranch(context.Background(), branchRequest(t, s, "switch", "base"))
	if err != nil || result.Branch != "base" {
		t.Fatalf("switch: %+v %v", result, err)
	}
	if got := strings.TrimSpace(gitFixtureCommand(t, root, "branch", "--show-current")); got != "base" {
		t.Fatal(got)
	}
	if _, err := s.ChangeGitBranch(context.Background(), branchRequest(t, s, "create", "feature/new")); err == nil || err.(Error).Code != "branch_exists" {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := s.ChangeGitBranch(context.Background(), branchRequest(t, s, "switch", "missing")); err == nil || err.(Error).Code != "branch_not_found" {
		t.Fatalf("missing: %v", err)
	}
	for _, name := range []string{"-f", "bad name", "@{-1}", "a..b", "a\nb", "refs/../bad"} {
		if _, err := s.ChangeGitBranch(context.Background(), branchRequest(t, s, "create", name)); err == nil || err.(Error).Status != 400 {
			t.Fatalf("invalid %q: %v", name, err)
		}
	}
}
func TestGitBranchChangesPreserveLocalFiles(t *testing.T) {
	s, root := branchFixture(t, true)
	gitFixtureCommand(t, root, "switch", "-c", "other")
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("other\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored.txt"), []byte("tracked on other\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitFixtureCommand(t, root, "add", "file.txt", "ignored.txt")
	gitFixtureCommand(t, root, "-c", "core.hooksPath="+os.DevNull, "commit", "-m", "other")
	gitFixtureCommand(t, root, "switch", "base")
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("local edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChangeGitBranch(context.Background(), branchRequest(t, s, "switch", "other")); err == nil || err.(Error).Code != "git_switch_rejected" {
		t.Fatalf("dirty switch: %v", err)
	}
	if content, _ := os.ReadFile(filepath.Join(root, "file.txt")); string(content) != "local edit\n" {
		t.Fatal("local edit lost")
	}
	if snapshot, _ := s.Git(context.Background(), "demo"); snapshot.Branch != "base" {
		t.Fatal("HEAD moved after refusal")
	}
	// Creating a branch at current HEAD can safely retain dirty edits.
	if _, err := s.ChangeGitBranch(context.Background(), branchRequest(t, s, "create", "keep-edit")); err != nil {
		t.Fatal(err)
	}
	if content, _ := os.ReadFile(filepath.Join(root, "file.txt")); string(content) != "local edit\n" {
		t.Fatal("create lost edit")
	}
	// Also protect ignored files that would be overwritten by a target branch.
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored.txt\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored.txt"), []byte("local ignored\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChangeGitBranch(context.Background(), branchRequest(t, s, "switch", "other")); err == nil {
		t.Fatal("overwrote ignored file")
	}
	if content, _ := os.ReadFile(filepath.Join(root, "ignored.txt")); string(content) != "local ignored\n" {
		t.Fatal("ignored data lost")
	}
}
func TestGitBranchUnbornDetachedWorktreeAndBoundary(t *testing.T) {
	s, root := branchFixture(t, false)
	if _, err := s.ChangeGitBranch(context.Background(), branchRequest(t, s, "create", "unborn")); err != nil {
		t.Fatal(err)
	}
	s, root = branchFixture(t, true)
	gitFixtureCommand(t, root, "checkout", "--detach")
	if _, err := s.ChangeGitBranch(context.Background(), branchRequest(t, s, "create", "from-detached")); err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(t.TempDir(), "linked")
	gitFixtureCommand(t, root, "worktree", "add", "-b", "occupied", tree)
	if _, err := s.ChangeGitBranch(context.Background(), branchRequest(t, s, "switch", "occupied")); err == nil {
		t.Fatal("switched to occupied branch")
	}
	def := s.Registry.(gitRegistry).def
	def.Workspace.Root = tree
	s.Registry = gitRegistry{def: def}
	if _, err := s.ChangeGitBranch(context.Background(), branchRequest(t, s, "create", "linked-new")); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	def.Workspace.Root = sub
	s.Registry = gitRegistry{def: def}
	list, err := s.GitBranches(context.Background(), "demo")
	if err != nil || list.CanChange || list.BlockedReason != "workspace_not_repo_root" {
		t.Fatalf("subdir: %+v %v", list, err)
	}
	if _, err := s.ChangeGitBranch(context.Background(), branchRequest(t, s, "switch", "base")); err == nil || err.(Error).Status != 403 {
		t.Fatalf("subdir mutation: %v", err)
	}
	def.Workspace.Root = root
	s.Registry = gitRegistry{def: def}
	s.ChatsRoot = filepath.Join(root, "chats")
	if _, err := s.ChangeGitBranch(context.Background(), branchRequest(t, s, "create", "no-chat-write")); err == nil || err.(Error).Status != 403 {
		t.Fatalf("chats boundary: %v", err)
	}
}
