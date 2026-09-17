package project

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"agent-platform/internal/catalog"
)

type gitRegistry struct {
	catalog.Registry
	def catalog.AgentDefinition
}

func (r gitRegistry) AgentDefinition(key string) (catalog.AgentDefinition, bool) {
	return r.def, key == r.def.Key
}
func gitFixtureCommand(t *testing.T, directory string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = directory
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}
func TestGitWorkspaceStates(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	gitFixtureCommand(t, root, "init", "-b", "actual-branch")
	result := probeGit(context.Background(), root)
	if result.Status != "branch" || result.Branch != "actual-branch" || result.Commit != "" {
		t.Fatalf("unborn: %+v", result)
	}
	gitFixtureCommand(t, root, "-c", "core.hooksPath="+os.DevNull, "commit", "--allow-empty", "-m", "fixture")
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"CODER", "KBASE", "OTHER"} {
		service := Service{Registry: gitRegistry{def: catalog.AgentDefinition{Key: "demo", Mode: mode, Workspace: catalog.AgentWorkspaceConfig{Root: sub}}}}
		result, err := service.Git(context.Background(), "demo")
		if err != nil || result.Status != "branch" || result.Branch != "actual-branch" || result.Commit == "" {
			t.Fatalf("%s subdirectory: %+v %v", mode, result, err)
		}
	}
	worktree := filepath.Join(t.TempDir(), "tree")
	gitFixtureCommand(t, root, "-c", "core.hooksPath="+os.DevNull, "worktree", "add", "-b", "worktree-branch", worktree)
	result = probeGit(context.Background(), worktree)
	if result.Status != "branch" || result.Branch != "worktree-branch" {
		t.Fatalf("worktree: %+v", result)
	}
	gitFixtureCommand(t, worktree, "-c", "core.hooksPath="+os.DevNull, "checkout", "--detach")
	result = probeGit(context.Background(), worktree)
	if result.Status != "detached" || result.Commit == "" || result.Branch != "" {
		t.Fatalf("detached: %+v", result)
	}
	// Host environment must not route discovery to another repository or branch.
	t.Setenv("GIT_DIR", filepath.Join(root, ".git"))
	t.Setenv("GIT_WORK_TREE", root)
	result = probeGit(context.Background(), worktree)
	if result.Status != "detached" {
		t.Fatalf("inherited Git environment leaked: %+v", result)
	}
	if result = probeGit(context.Background(), t.TempDir()); result.Status != "not_repository" {
		t.Fatalf("plain directory: %+v", result)
	}
}
func TestGitWorkspaceErrors(t *testing.T) {
	def := catalog.AgentDefinition{Key: "demo", Mode: "KBASE"}
	service := Service{Registry: gitRegistry{def: def}}
	result, err := service.Git(context.Background(), "demo")
	if err != nil || result.Status != "no_workspace" {
		t.Fatalf("no workspace: %+v %v", result, err)
	}
	for _, key := range []string{"", "missing"} {
		if _, err := service.Git(context.Background(), key); err == nil {
			t.Fatalf("expected error for %q", key)
		}
	}
	def.Workspace.Root = filepath.Join(t.TempDir(), "missing")
	service.Registry = gitRegistry{def: def}
	result, err = service.Git(context.Background(), "demo")
	if err != nil || result.Status != "unavailable" || result.Reason != "workspace_unavailable" {
		t.Fatalf("missing directory: %+v %v", result, err)
	}
	chats := t.TempDir()
	def.Workspace.Root = chats
	service.Registry, service.ChatsRoot = gitRegistry{def: def}, chats
	result, err = service.Git(context.Background(), "demo")
	if err == nil && result.Status != "unavailable" {
		t.Fatalf("chat root accepted: %+v", result)
	}
	// Preserve filesystem permission errors as API errors.
	mapped := mapFilesystemError(os.ErrPermission, "missing").(Error)
	if mapped.Status != http.StatusForbidden {
		t.Fatal(mapped)
	}
}
func TestGitProbeFailures(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, ".git"), []byte("gitdir: missing\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// A broken .git link is a read failure, not a normal non-repository directory.
	result := probeGit(context.Background(), broken)
	if result.Status != "unavailable" {
		t.Fatalf("broken gitdir: %+v", result)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result := probeGit(ctx, t.TempDir()); result.Status != "unavailable" {
		t.Fatalf("cancelled: %+v", result)
	}
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	if result := probeGit(expired, t.TempDir()); result.Status != "unavailable" || result.Reason != "probe_timeout" {
		t.Fatalf("expired deadline: %+v", result)
	}
	t.Setenv("PATH", t.TempDir())
	if result := probeGit(context.Background(), t.TempDir()); result.Reason != "git_unavailable" {
		t.Fatalf("missing git: %+v", result)
	}
}
