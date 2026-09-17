package project

import (
	"context"
	"crypto/sha256"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/pathutil"
)

// Bounded process-wide locks also serialize different Agents sharing a repo.
var gitBranchLocks [64]sync.Mutex

func gitOperationError(status int, code, message string) error {
	return Error{Status: status, Code: code, Message: message}
}

func (s Service) gitWorkspace(agentKey string) (workspace, error) {
	if s.Registry == nil {
		return workspace{}, gitOperationError(503, "unavailable", "agent registry is not configured")
	}
	if strings.TrimSpace(agentKey) == "" {
		return workspace{}, gitOperationError(400, "invalid_request", "agentKey is required")
	}
	def, ok := s.Registry.AgentDefinition(strings.TrimSpace(agentKey))
	if !ok {
		return workspace{}, gitOperationError(404, "not_found", "agent not found")
	}
	return s.resolveDefinitionWorkspace(def)
}

// Branch changes affect the entire worktree. A workspace that only owns a
// subdirectory may read HEAD, but cannot mutate files outside its boundary.
func gitChangeBlocked(ctx context.Context, ws workspace) string {
	top, err := runProjectGit(ctx, ws.roots.Workspace.Host, "rev-parse", "--show-toplevel")
	if err != nil {
		return "worktree_unavailable"
	}
	canonical, err := pathutil.Canonicalize(top)
	if err != nil {
		return "worktree_unavailable"
	}
	if canonical.Key != ws.roots.Workspace.Key {
		return "workspace_not_repo_root"
	}
	if ws.roots.WorkspaceContainsChatsRoot() {
		return "workspace_contains_chats"
	}
	return ""
}

func (s Service) GitBranches(ctx context.Context, agentKey string) (api.ProjectGitBranchesResponse, error) {
	result := api.ProjectGitBranchesResponse{Branches: []string{}}
	ws, err := s.gitWorkspace(agentKey)
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	output, err := runProjectGit(ctx, ws.roots.Workspace.Host, "for-each-ref", "--format=%(refname:strip=2)", "--sort=refname", "refs/heads/")
	if err != nil {
		return result, gitOperationError(503, "git_unavailable", "cannot read local Git branches")
	}
	if output != "" {
		result.Branches = strings.Split(output, "\n")
	}
	result.Git = probeGit(ctx, ws.roots.Workspace.Host)
	result.Git.AgentKey = strings.TrimSpace(agentKey)
	setGitRevision(&result.Git, ws.roots.Workspace.Host)
	if result.Git.Status != "branch" && result.Git.Status != "detached" {
		return result, gitOperationError(503, "git_unavailable", "cannot read Git HEAD")
	}
	// An unborn branch has no refs/heads entry yet.
	if result.Git.Status == "branch" && result.Git.Commit == "" {
		result.Branches = append(result.Branches, result.Git.Branch)
	}
	result.BlockedReason = gitChangeBlocked(ctx, ws)
	result.CanChange = result.BlockedReason == ""
	if strings.EqualFold(ws.definition.Mode, catalog.AgentModeCoder) {
		result.ExpectedBranch = ws.definition.Project.Git.ExpectedBranch
	}
	return result, nil
}

func (s Service) ChangeGitBranch(ctx context.Context, request api.ProjectGitBranchRequest) (api.ProjectGitResponse, error) {
	result := api.ProjectGitResponse{}
	if request.Operation != "switch" && request.Operation != "create" {
		return result, gitOperationError(400, "invalid_request", "operation must be switch or create")
	}
	branch := request.Branch
	if branch == "" || len(branch) > 255 || strings.HasPrefix(branch, "-") || strings.ContainsAny(branch, "\x00\r\n") || strings.TrimSpace(branch) != branch || request.ExpectedRevision == "" {
		return result, gitOperationError(400, "invalid_request", "branch and expectedRevision are required; branch must be a valid local branch name")
	}
	ws, err := s.gitWorkspace(request.AgentKey)
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	dir := ws.roots.Workspace.Host
	if _, err := runProjectGit(ctx, dir, "check-ref-format", "refs/heads/"+branch); err != nil {
		return result, gitOperationError(400, "invalid_branch", "invalid Git branch name")
	}
	common, err := runProjectGit(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return result, gitOperationError(503, "git_unavailable", "cannot resolve Git repository")
	}
	canonical, err := pathutil.Canonicalize(common)
	if err != nil {
		return result, gitOperationError(503, "git_unavailable", "cannot resolve Git repository")
	}
	hash := sha256.Sum256([]byte(canonical.Key))
	lock := &gitBranchLocks[int(hash[0])%len(gitBranchLocks)]
	if !lock.TryLock() {
		return result, gitOperationError(409, "git_busy", "another branch operation is in progress; refresh and retry")
	}
	defer lock.Unlock()
	// Resolve the Agent again after acquiring the repository operation lock.
	currentWS, err := s.gitWorkspace(request.AgentKey)
	if err != nil {
		return result, err
	}
	if currentWS.roots.Workspace.Key != ws.roots.Workspace.Key {
		return result, gitOperationError(409, "revision_conflict", "project directory changed; refresh and retry")
	}
	if reason := gitChangeBlocked(ctx, currentWS); reason != "" {
		return result, gitOperationError(http.StatusForbidden, reason, "branch changes require a complete worktree within the project boundary")
	}
	result, err = s.Git(ctx, request.AgentKey)
	if err != nil {
		return result, err
	}
	if result.Revision == "" || request.ExpectedRevision != result.Revision {
		return result, gitOperationError(409, "revision_conflict", "Git HEAD changed; refresh and retry")
	}
	if request.Operation == "switch" && result.Status == "branch" && result.Branch == branch {
		return result, nil
	}
	_, existsErr := runProjectGit(ctx, dir, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if request.Operation == "switch" && existsErr != nil {
		return result, gitOperationError(409, "branch_not_found", "local branch no longer exists; refresh and retry")
	}
	if request.Operation == "create" && (existsErr == nil || result.Branch == branch) {
		return result, gitOperationError(409, "branch_exists", "a branch with that name already exists")
	}
	// Keep Git's own dirty-file/worktree protections. Never force, stash, clean,
	// guess remote branches, recurse submodules, overwrite ignored files or run hooks.
	args := []string{"-c", "core.hooksPath=" + os.DevNull, "switch", "--no-guess", "--no-recurse-submodules", "--no-overwrite-ignore"}
	if request.Operation == "create" {
		args = append(args, "-c", branch)
	} else {
		args = append(args, "--", branch)
	}
	output, err := runProjectGit(ctx, dir, args...)
	if err != nil {
		if ctx.Err() != nil {
			return result, gitOperationError(503, "git_operation_uncertain", "branch operation timed out; refresh Git state before retrying")
		}
		if len(output) > 4096 {
			output = output[:4096]
		}
		return result, gitOperationError(409, "git_switch_rejected", output)
	}
	result, err = s.Git(ctx, request.AgentKey)
	if err != nil || result.Status != "branch" || result.Branch != branch {
		return result, gitOperationError(503, "git_operation_uncertain", "branch command finished; refresh Git state to verify the result")
	}
	return result, nil
}
