package tools

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/agentconfig"
	. "agent-platform/internal/contracts"
)

func (t *RuntimeToolExecutor) invokeSandboxBash(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	command := strings.TrimSpace(stringArg(args, "command"))
	if command == "" {
		return ToolExecutionResult{Output: "Missing argument: command", Error: "missing_command", ExitCode: -1}, nil
	}
	invocationEnv := stringMapArg(args, "env")
	if err := agentconfig.ValidateUserEnvironment(invocationEnv); err != nil {
		return ToolExecutionResult{Output: err.Error(), Error: "reserved_environment_variable", ExitCode: -1}, nil
	}
	cwd, err := resolveSandboxCwd(execCtx, stringArg(args, "cwd"))
	if err != nil {
		code := "sandbox_invalid_cwd"
		if strings.Contains(err.Error(), "workspace_unavailable") {
			code = "workspace_unavailable"
		}
		return ToolExecutionResult{Output: err.Error(), Error: code, ExitCode: -1}, nil
	}
	session := accessPolicySession(execCtx)
	reviewCwd := strings.TrimSpace(stringArg(args, "cwd"))
	if reviewCwd == "" {
		reviewCwd = "@workspace"
	}
	accessReview := accesspolicy.ReviewBashCommand(
		t.cfg.AccessPolicy,
		session,
		command,
		reviewCwd,
		bashSecurityKnownVariables(execCtx),
	)
	switch accessReview.Decision {
	case accesspolicy.DecisionAllow, accesspolicy.DecisionAutoApproved:
	case accesspolicy.DecisionRequiresApproval:
		if !accesspolicy.ConsumeApproval(execCtx, accessReview) {
			return ToolExecutionResult{Output: accessReview.Reason, Error: "bash_access_approval_required", ExitCode: -1}, nil
		}
	default:
		return ToolExecutionResult{Output: accessReview.Reason, Error: "bash_access_blocked", ExitCode: -1}, nil
	}
	timeout := t.resolveBashTimeoutSeconds(args, execCtx)
	result, err := t.sandbox.Execute(ctx, execCtx, command, cwd, timeout, invocationEnv)
	if err != nil {
		return ToolExecutionResult{Output: err.Error(), Error: "sandbox_execute_failed", ExitCode: -1}, nil
	}
	return bashResult(result.Stdout, result.Stderr, "sandbox", result.Cwd, result.ExitCode, ""), nil
}

func resolveSandboxCwd(execCtx *ExecutionContext, raw string) (string, error) {
	if execCtx == nil {
		return "", fmt.Errorf("workspace_unavailable: sandbox execution context is required")
	}
	workspace := strings.TrimSpace(execCtx.Session.RuntimeContext.SandboxPaths.WorkspaceDir)
	chatDir := strings.TrimSpace(execCtx.Session.RuntimeContext.SandboxPaths.ChatDir)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "@workspace"
	}
	for _, item := range []struct {
		alias string
		root  string
	}{
		{alias: "@workspace", root: workspace},
		{alias: "@chat", root: chatDir},
	} {
		if strings.EqualFold(raw, item.alias) {
			if item.root == "" {
				return "", fmt.Errorf("%s_unavailable: %s is required", strings.TrimPrefix(item.alias, "@"), strings.TrimPrefix(item.alias, "@"))
			}
			return item.root, nil
		}
		prefix := item.alias + "/"
		if strings.HasPrefix(strings.ToLower(filepath.ToSlash(raw)), prefix) {
			if item.root == "" {
				return "", fmt.Errorf("%s_unavailable: %s is required", strings.TrimPrefix(item.alias, "@"), strings.TrimPrefix(item.alias, "@"))
			}
			suffix := filepath.ToSlash(raw)[len(prefix):]
			return joinExecutionRoot(item.root, suffix)
		}
	}
	if path.IsAbs(raw) || filepath.IsAbs(raw) {
		if strings.HasPrefix(raw, "/") {
			return path.Clean(raw), nil
		}
		return filepath.Clean(raw), nil
	}
	if workspace == "" {
		return "", fmt.Errorf("workspace_unavailable: relative cwd requires a workspace")
	}
	return joinExecutionRoot(workspace, filepath.ToSlash(raw))
}

func joinExecutionRoot(root string, suffix string) (string, error) {
	var resolved string
	var rel string
	var err error
	if strings.HasPrefix(root, "/") {
		resolved = path.Clean(path.Join(root, suffix))
		rel, err = filepath.Rel(filepath.FromSlash(root), filepath.FromSlash(resolved))
	} else {
		resolved = filepath.Clean(filepath.Join(root, filepath.FromSlash(suffix)))
		rel, err = filepath.Rel(root, resolved)
	}
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("cwd escapes its declared root")
	}
	return resolved, nil
}
