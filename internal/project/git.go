package project

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"agent-platform/internal/api"
)

// Git only resolves the configured workspace; callers cannot supply a filesystem path.
func (s Service) Git(ctx context.Context, agentKey string) (api.ProjectGitResponse, error) {
	agentKey = strings.TrimSpace(agentKey)
	result := api.ProjectGitResponse{AgentKey: agentKey}
	if agentKey == "" {
		return result, Error{Status: http.StatusBadRequest, Code: "invalid_request", Message: "agentKey is required"}
	}
	if s.Registry == nil {
		return result, Error{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: "agent registry is not configured"}
	}
	def, ok := s.Registry.AgentDefinition(agentKey)
	if !ok {
		return result, Error{Status: http.StatusNotFound, Code: "not_found", Message: "agent not found"}
	}
	if strings.TrimSpace(def.Workspace.Root) == "" {
		result.Status = "no_workspace"
		return result, nil
	}
	ws, err := s.resolveDefinitionWorkspace(def)
	if err != nil {
		var projectErr Error
		if errors.As(err, &projectErr) && projectErr.Status == http.StatusForbidden {
			return result, err
		}
		result.Status, result.Reason = "unavailable", "workspace_unavailable"
		return result, nil
	}
	snapshot := probeGit(ctx, ws.roots.Workspace.Host)
	snapshot.AgentKey = agentKey
	return snapshot, nil
}

// No shell, hooks, index refresh, status scan or writes. Strip inherited Git
// routing/config variables so the result always belongs to this workspace.
func probeGit(ctx context.Context, directory string) (result api.ProjectGitResponse) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	defer func() {
		if result.Status == "unavailable" && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result.Reason = "probe_timeout"
		}
	}()
	unavailable := api.ProjectGitResponse{Status: "unavailable", Reason: "probe_failed"}
	env := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		key := strings.ToUpper(strings.SplitN(entry, "=", 2)[0])
		if !strings.HasPrefix(key, "GIT_") && key != "LC_ALL" && key != "LANGUAGE" {
			env = append(env, entry)
		}
	}
	env = append(env, "LC_ALL=C", "GIT_OPTIONAL_LOCKS=0")
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir, cmd.Env = directory, env
		cmd.WaitDelay = 100 * time.Millisecond
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if output, err := run("rev-parse", "--absolute-git-dir"); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			unavailable.Reason = "git_unavailable"
		}
		if ctx.Err() != nil {
			unavailable.Reason = "probe_timeout"
		} else if strings.HasPrefix(output, "fatal: not a git repository (or any of the parent directories)") && !hasGitMarker(directory) {
			return api.ProjectGitResponse{Status: "not_repository"}
		}
		return unavailable
	}
	// Retry once if HEAD moves between reads. Never join a branch name with a
	// commit obtained from a different observed HEAD.
	for attempt := 0; attempt < 2; attempt++ {
		ref, refErr := run("symbolic-ref", "--quiet", "HEAD")
		detached := false
		if refErr != nil {
			var exitErr *exec.ExitError
			if !errors.As(refErr, &exitErr) || exitErr.ExitCode() != 1 {
				return unavailable
			}
			detached = true
		} else if !strings.HasPrefix(ref, "refs/heads/") {
			return unavailable
		}
		commit, commitErr := run("rev-parse", "--verify", "HEAD^{commit}")
		if commitErr != nil {
			if detached {
				return unavailable
			}
			// Missing branch ref is an unborn branch; a present but broken ref is an error.
			_, err := run("show-ref", "--verify", "--quiet", ref)
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				return unavailable
			}
			commit = ""
		}
		nextRef, nextErr := run("symbolic-ref", "--quiet", "HEAD")
		if ctx.Err() != nil {
			return unavailable
		}
		if nextErr != nil {
			var exitErr *exec.ExitError
			if !errors.As(nextErr, &exitErr) || exitErr.ExitCode() != 1 {
				return unavailable
			}
		}
		if nextRef != ref || (nextErr == nil) != (refErr == nil) {
			continue
		}
		if detached {
			return api.ProjectGitResponse{Status: "detached", Commit: commit}
		}
		return api.ProjectGitResponse{Status: "branch", Branch: strings.TrimPrefix(ref, "refs/heads/"), Commit: commit}
	}
	if ctx.Err() != nil {
		unavailable.Reason = "probe_timeout"
	}
	return unavailable
}

// Git uses a similar discovery error for corrupt repositories. A .git marker
// or an unreadable ancestor must never be labelled a plain non-Git directory.
func hasGitMarker(directory string) bool {
	for {
		_, err := os.Lstat(filepath.Join(directory, ".git"))
		if err == nil || !errors.Is(err, os.ErrNotExist) {
			return true
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return false
		}
		directory = parent
	}
}
