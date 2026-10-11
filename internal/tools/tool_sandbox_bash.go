package tools

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/agentconfig"
	"agent-platform/internal/bashsec"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/pathutil"
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
	accessReview := t.ReviewBashAccess(ctx, args, execCtx, t.cfg.AccessPolicy)
	if security := accessReview.SecurityReview(command, bashSecurityKnownVariables(execCtx)); security.Decision == bashsec.ReviewBlock {
		return ToolExecutionResult{Output: security.Reason, Error: "bash_security_blocked", ExitCode: -1}, nil
	}
	approvalSource := accesspolicy.BashApprovalSource(execCtx, accessReview)
	accessReview = accesspolicy.PendingBashPlan(execCtx, accessReview)
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
	output := bashResult(result.Stdout, result.Stderr, "sandbox", result.Cwd, result.ExitCode, "")
	if approvalSource != "" || accessReview.HasConnector || accessReview.AutoApproved() || accessReview.RuleKey == "bash-access:authored-script" || accessReview.RuleKey == "bash-access:temp-script" || accessReview.RuleKey == "bash-access:skill-script" {
		if output.Structured == nil {
			output.Structured = map[string]any{"stdout": result.Stdout, "stderr": result.Stderr, "mode": "sandbox", "cwd": result.Cwd, "exitCode": result.ExitCode}
		}
		output.Structured["accessPolicy"] = accesspolicy.BashPlanMetadata(accessReview)
		if approvalSource != "" {
			metadata := output.Structured["accessPolicy"].(map[string]any)
			metadata["decision"] = "allow"
			metadata["approvalSource"] = approvalSource
		}
	}
	return output, nil
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
	if strings.HasPrefix(raw, "@skills/") && execCtx.Session.SkillDirs != nil {
		if _, err := accesspolicy.ResolveSessionPath(execCtx.Session, raw); err != nil {
			return "", err
		}
		return "/skills/" + strings.TrimPrefix(raw, "@skills/"), nil
	}
	if slashed := filepath.ToSlash(raw); strings.EqualFold(slashed, "@runtime") || strings.HasPrefix(strings.ToLower(slashed), "@runtime/") {
		return resolveSandboxRuntimeCwd(execCtx.Session, slashed)
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

// resolveSandboxRuntimeCwd maps an @runtime path to an existing container
// mount. It never passes a Host path through and never adds a mount.
func resolveSandboxRuntimeCwd(session QuerySession, raw string) (string, error) {
	host, err := accesspolicy.ResolveSessionPath(session, raw)
	if err != nil {
		return "", err
	}
	target, err := pathutil.Canonicalize(host)
	if err != nil {
		return "", err
	}
	local, guest := session.RuntimeContext.LocalPaths, session.RuntimeContext.SandboxPaths
	best, bestRoot := "", pathutil.Canonical{}
	for _, mount := range []struct{ host, guest string }{
		{accesspolicy.SessionWorkspaceRoot(session), guest.WorkspaceDir},
		{accesspolicy.SessionChatDir(session), guest.ChatDir},
		{local.SkillsDir, guest.SkillsDir},
		{local.AgentDir, guest.AgentDir},
		{local.OwnerDir, guest.OwnerDir},
		{local.SkillsCenterDir, guest.SkillsCenterDir},
		{local.PanDir, guest.PanDir},
	} {
		if strings.TrimSpace(mount.host) == "" || strings.TrimSpace(mount.guest) == "" {
			continue
		}
		root, err := pathutil.Canonicalize(mount.host)
		if err != nil || !pathutil.WithinRoot(target, root) {
			continue
		}
		// Prefer the most specific mount when mounts are nested.
		if len(root.Host) > len(bestRoot.Host) {
			best, bestRoot = strings.TrimSpace(mount.guest), root
		}
	}
	if best == "" {
		return "", fmt.Errorf("runtime_path_not_mounted: %s is not inside a directory mounted into the container", raw)
	}
	rel, err := filepath.Rel(bestRoot.Host, target.Host)
	if err != nil {
		return "", fmt.Errorf("cwd escapes its declared root")
	}
	return joinExecutionRoot(best, filepath.ToSlash(rel))
}
