package tools

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/agentconfig"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

// ReviewBashAccess preserves the runtime's execution-environment-aware review
// through the router used by the LLM engine in production.
func (r *ToolRouter) ReviewBashAccess(ctx context.Context, args map[string]any, execCtx *ExecutionContext, cfg config.AccessPolicyConfig) accesspolicy.BashPlan {
	if reviewer, ok := r.runtime.(interface {
		ReviewBashAccess(context.Context, map[string]any, *ExecutionContext, config.AccessPolicyConfig) accesspolicy.BashPlan
	}); ok {
		return reviewer.ReviewBashAccess(ctx, args, execCtx, cfg)
	}
	return accesspolicy.ReviewBashCommand(cfg, accessPolicySession(execCtx), stringArg(args, "command"), stringArg(args, "cwd"), bashSecurityKnownVariables(execCtx), execCtx)
}

// ReviewBashAccess is shared by preflight and both executors. Container probes
// only resolve/read targets; they never execute the requested program.
func (t *RuntimeToolExecutor) ReviewBashAccess(ctx context.Context, args map[string]any, execCtx *ExecutionContext, cfg config.AccessPolicyConfig) accesspolicy.BashPlan {
	session := accessPolicySession(execCtx)
	vars := bashSecurityKnownVariables(execCtx)
	var environment *accesspolicy.BashEnvironment
	if !session.AgentHasRuntimeSandbox {
		actual, err := mergeCommandEnv(execCtx)
		if err != nil {
			return accesspolicy.BashPlan{Decision: accesspolicy.DecisionBlock, Reason: err.Error()}
		}
		vars = bashEnvironmentVariables(actual)
	}
	if session.AgentHasRuntimeSandbox {
		invocationEnv, err := sandboxInvocationEnvArg(args)
		if err != nil {
			return accesspolicy.BashPlan{Decision: accesspolicy.DecisionBlock, Reason: err.Error()}
		}
		vars = agentconfig.Merge(vars, invocationEnv, agentconfig.ContainerEnvironment(session.RuntimeContext.SandboxPaths.AgentDir, session.RuntimeContext.SandboxPaths.WorkspaceDir, session.RuntimeContext.SandboxPaths.ChatDir))
		environment = t.sandboxBashEnvironment(ctx, execCtx)
	}
	return accesspolicy.ReviewBashCommandInEnvironment(cfg, session, stringArg(args, "command"), stringArg(args, "cwd"), vars, environment, execCtx)
}

func bashEnvironmentVariables(env []string) map[string]string {
	vars := map[string]string{}
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && !strings.EqualFold(key, agentconfig.EnvAccessToken) {
			vars[key] = value
		}
	}
	return vars
}

func (t *RuntimeToolExecutor) sandboxBashEnvironment(ctx context.Context, execCtx *ExecutionContext) *accesspolicy.BashEnvironment {
	type inspection struct {
		header, hash string
		err          error
	}
	inspections := map[string]inspection{}
	resolutions := map[string]string{}
	directories := map[string]string{}
	guestPath := func(raw string) string {
		for _, root := range []struct{ host, guest string }{
			{accesspolicy.SessionWorkspaceRoot(execCtx.Session), execCtx.Session.RuntimeContext.SandboxPaths.WorkspaceDir},
			{accesspolicy.SessionChatDir(execCtx.Session), execCtx.Session.RuntimeContext.SandboxPaths.ChatDir},
			{execCtx.Session.TempRoot, "/tmp"},
		} {
			if root.host == "" || root.guest == "" {
				continue
			}
			rel, err := filepath.Rel(root.host, raw)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return strings.TrimRight(root.guest, "/") + "/" + filepath.ToSlash(rel)
			}
		}
		return raw
	}
	probe := func(command, cwd string, env map[string]string) (string, error) {
		if t.sandbox == nil {
			return "", fmt.Errorf("container resolver unavailable")
		}
		if err := t.sandbox.OpenIfNeeded(ctx, execCtx); err != nil {
			return "", err
		}
		probeEnv := map[string]string{}
		for key, value := range env {
			if !agentconfig.IsReserved(key) {
				probeEnv[key] = value
			}
		}
		// No live output sink: these are policy probes, not user tool output.
		cloned := *execCtx
		cloned.ToolOutputSink = nil
		result, err := t.sandbox.Execute(ctx, &cloned, command, guestPath(cwd), 10, probeEnv)
		if err != nil {
			return "", err
		}
		if result.ExitCode != 0 {
			return "", fmt.Errorf("container target could not be inspected")
		}
		return result.Stdout, nil
	}
	checkTempEscape := func(raw, cwd, canonical string) error {
		lexical := guestPath(raw)
		if !path.IsAbs(lexical) {
			lexical = path.Join(guestPath(cwd), lexical)
		}
		lexical = path.Clean(lexical)
		insideTemp := func(p string) bool { return p == "/tmp" || strings.HasPrefix(p, "/tmp/") }
		if insideTemp(lexical) && !insideTemp(canonical) {
			return accesspolicy.ErrBashTemporaryEscape
		}
		return nil
	}
	return &accesspolicy.BashEnvironment{
		Directory: func(raw string) (string, error) {
			target := guestPath(raw)
			if canonical, exists := directories[target]; exists {
				return canonical, nil
			}
			out, err := probe("test -d "+shellQuoteProbe(target)+" && /usr/bin/readlink -f -- "+shellQuoteProbe(target), "/tmp", nil)
			canonical := strings.TrimSpace(out)
			if err != nil || !strings.HasPrefix(canonical, "/") || strings.Contains(canonical, "\n") {
				return "", fmt.Errorf("container directory unresolved")
			}
			if err := checkTempEscape(target, "/tmp", canonical); err != nil {
				return "", err
			}
			directories[target] = canonical
			return canonical, nil
		},
		Canonical: func(raw, cwd string) (string, error) {
			target := guestPath(raw)
			out, err := probe("test -f "+shellQuoteProbe(target)+" && /usr/bin/readlink -f -- "+shellQuoteProbe(target), cwd, nil)
			resolved := strings.TrimSpace(out)
			if err != nil || !strings.HasPrefix(resolved, "/") || strings.Contains(resolved, "\n") {
				return "", fmt.Errorf("container script unresolved")
			}
			if err := checkTempEscape(target, cwd, resolved); err != nil {
				return "", err
			}
			return resolved, nil
		},
		Resolve: func(name, cwd string, env map[string]string) (string, error) {
			key := cwd + "\x00" + name + "\x00" + fmt.Sprint(env)
			if p, ok := resolutions[key]; ok {
				return p, nil
			}
			target := name
			if strings.Contains(name, "/") {
				target = guestPath(name)
			}
			// readlink is absolute so a workspace PATH entry cannot forge the probe.
			out, err := probe("p=$(command -v -- "+shellQuoteProbe(target)+") && test -f \"$p\" && printf '%s\\n' \"$p\" && /usr/bin/readlink -f -- \"$p\"", cwd, env)
			lexical, p, twoLines := strings.Cut(strings.TrimSpace(out), "\n")
			if !twoLines {
				p = lexical
			}
			if err != nil || !strings.HasPrefix(p, "/") || strings.Contains(p, "\n") {
				return "", fmt.Errorf("container executable unresolved")
			}
			if err := checkTempEscape(lexical, cwd, p); err != nil {
				return "", err
			}
			resolutions[key] = p
			return p, nil
		},
		Inspect: func(raw string) (string, string, error) {
			path := guestPath(raw)
			if found, ok := inspections[path]; ok {
				return found.header, found.hash, found.err
			}
			// Bounded header, full digest; non-regular targets are not scripts.
			out, err := probe("test -f "+shellQuoteProbe(path)+" && /usr/bin/sha256sum -- "+shellQuoteProbe(path)+" && /usr/bin/head -c 4096 -- "+shellQuoteProbe(path), "/tmp", nil)
			line, header, ok := strings.Cut(out, "\n")
			fields := strings.Fields(line)
			hash := ""
			if err == nil && ok && len(fields) > 0 && len(fields[0]) == 64 {
				hash = fields[0]
			} else {
				err = fmt.Errorf("container content unavailable")
			}
			inspections[path] = inspection{header, hash, err}
			return header, hash, err
		},
	}
}

func shellQuoteProbe(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
