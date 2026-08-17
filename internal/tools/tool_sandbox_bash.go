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
	invocationEnv, err := sandboxInvocationEnvArg(args)
	if err != nil {
		return ToolExecutionResult{Output: err.Error(), Error: "invalid_environment", ExitCode: -1}, nil
	}
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

func sandboxInvocationEnvArg(args map[string]any) (map[string]string, error) {
	raw, exists := args["env"]
	if !exists {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("env must be an array of name/value objects")
	}
	values := make(map[string]string, len(items))
	for index, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("env[%d] must be an object", index)
		}
		for key := range item {
			if key != "name" && key != "value" {
				return nil, fmt.Errorf("env[%d] contains unsupported field %q", index, key)
			}
		}
		name, nameOK := item["name"].(string)
		if !nameOK || !validSandboxEnvironmentName(name) {
			return nil, fmt.Errorf("env[%d].name must match [A-Za-z_][A-Za-z0-9_]*", index)
		}
		value, valueOK := item["value"].(string)
		if !valueOK {
			return nil, fmt.Errorf("env[%d].value must be a string", index)
		}
		if _, duplicate := values[name]; duplicate {
			return nil, fmt.Errorf("env contains duplicate variable %q", name)
		}
		values[name] = value
	}
	if len(values) == 0 {
		return nil, nil
	}
	return values, nil
}

func validSandboxEnvironmentName(name string) bool {
	if name == "" {
		return false
	}
	for index := 0; index < len(name); index++ {
		char := name[index]
		if (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || char == '_' {
			continue
		}
		if index > 0 && char >= '0' && char <= '9' {
			continue
		}
		return false
	}
	return true
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
		{alias: "@temp", root: "/tmp"},
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
