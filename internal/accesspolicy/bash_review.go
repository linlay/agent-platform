package accesspolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"

	"agent-platform/internal/bashast"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/shellanalysis"
)

func reviewBashExecution(cfg config.AccessPolicyConfig, session QuerySession, command, cwd string, variables map[string]string, environment *BashEnvironment, contexts ...*ExecutionContext) BashPlan {
	accessLevel := sessionAccessLevel(session)
	level := EffectiveLevel(cfg, accessLevel)
	command = strings.TrimSpace(command)
	if command == "" {
		return BashPlan{Decision: DecisionAllow}
	}
	if strings.TrimSpace(cwd) == "" {
		cwd = "@workspace"
	}
	workingDir, err := ResolveSessionPath(session, cwd)
	if err != nil {
		return bashPlan(command, accessLevel, DecisionBlock, err.Error(), "bash-access:cwd", cwd)
	}
	workingDir, err = NormalizePath(workingDir)
	if err != nil {
		return bashPlan(command, accessLevel, DecisionBlock, err.Error(), "bash-access:cwd", cwd)
	}
	var execCtx *ExecutionContext
	if len(contexts) > 0 {
		execCtx = contexts[0]
	}
	var plans []BashPlan
	add := func(p BashPlan) {
		if p.Decision != DecisionAllow || p.RuleKey == "bash-access:temp-script" || p.RuleKey == "bash-access:authored-script" || p.RuleKey == "bash-access:skill-script" {
			plans = append(plans, p)
		}
	}
	pathReview := func(mode AccessMode, raw string) {
		if containsUnresolvedPlaceholder(raw) {
			add(bashPlanForAction(command, accessLevel, level.Approvals.BashComplexFilesystem, "bash path cannot be resolved statically", "bash-access:complex"))
			return
		}
		p, err := BuildPathPlan(cfg, session, mode, raw)
		if err != nil {
			add(bashPlan(command, accessLevel, DecisionBlock, err.Error(), "bash-access:path-invalid", raw))
			return
		}
		if mode == WriteAccess && session.ScopedFilePolicy != nil && !session.ScopedFilePolicy.WorkspaceMutationEnabled && PathInSessionWorkspace(session, p.Path) {
			add(bashPlan(command, accessLevel, DecisionBlock, "KBASE workspace mutation requires editingMode=true", "bash-access:kbase-mutation", p.Path))
			return
		}
		reason := "bash path is outside allowed roots: " + p.Path
		if p.Blocked() {
			reason = "bash path blocked: " + p.Reason + ": " + p.Path
		}
		add(reviewBashPathPlan(command, accessLevel, level, mode, p, reason))
	}
	if session.AgentHasRuntimeSandbox && environment != nil && environment.Directory != nil {
		if canonical, err := environment.Directory(workingDir); err == nil {
			workingDir = canonical
		} else if errors.Is(err, ErrBashTemporaryEscape) {
			add(bashPlan(command, accessLevel, DecisionBlock, err.Error(), "bash-access:temp-escape", workingDir))
		} else {
			add(bashPlanForAction(command, accessLevel, level.Approvals.BashComplexFilesystem, "container working directory cannot be resolved", "bash-access:complex"))
		}
	}
	parsed := bashast.ParseForSecurityWithKnownVariables(command, variables)
	if len(session.ConnectorCLIEntries) > 0 {
		parsed = bashast.ParseForExecution(command, variables)
	}
	if parsed.Kind != bashast.Simple {
		pathReview(ReadAccess, workingDir)
		add(bashPlanForAction(command, accessLevel, level.Approvals.BashComplexFilesystem, "bash command is too complex for access-policy path analysis", "bash-access:complex"))
		return combineBashPlans(command, accessLevel, plans)
	}
	var connectorWords []bashast.WordSpan
	onlyConnectors := len(parsed.Commands) > 0
	if len(parsed.Commands) == 0 {
		pathReview(ReadAccess, workingDir)
	}
	possibleCwds := []string{workingDir}
	for _, cmd := range parsed.Commands {
		allConnector := len(cmd.Words) > 0
		for _, candidateCwd := range append([]string(nil), possibleCwds...) {
			x := analyzeBashExecution(session, cmd, candidateCwd, variables, environment)
			if x.BlockReason != "" {
				add(bashPlan(command, accessLevel, DecisionBlock, x.BlockReason, "bash-access:temp-escape", cmd.Text))
			}
			allConnector = allConnector && x.Connector
			if !x.Connector {
				pathReview(ReadAccess, candidateCwd)
				pathReview(ReadAccess, x.Cwd)
				if x.Program != "" {
					pathReview(ReadAccess, x.Program)
				}
				if x.Script != "" {
					pathReview(ReadAccess, resolveAgainstCwd(x.Script, x.Cwd))
				}
				if x.Uncertain {
					add(bashPlanForAction(command, accessLevel, level.Approvals.BashComplexFilesystem, "bash execution wrapper or target cannot be resolved statically", "bash-access:complex"))
				} else if x.Opaque {
					exempt := false
					if decisionForAction(level.Approvals.BashOpaqueCommand) == DecisionBlock {
						add(opaqueBashPlan(command, accessLevel, level.Approvals.BashOpaqueCommand, x.Identity, x.Cwd))
					}
					if x.Script != "" && execCtx != nil && skillExecutionMatches(session, execCtx, resolveAgainstCwd(x.Script, x.Cwd), environment) {
						add(bashPlan(command, accessLevel, DecisionAllow, "script matches this run's selected skill", "bash-access:skill-script", x.Script))
						exempt = true
					}
					if !exempt {
						add(executionFingerprint(opaqueBashPlan(command, accessLevel, level.Approvals.BashOpaqueCommand, x.Identity, x.Cwd), x, variables, environment))
					}
				}
			}
			for _, redirect := range cmd.Redirects {
				kind := classifyRedirectAccess(redirect)
				if kind == redirectAccessNeutral {
					continue
				}
				if kind == redirectAccessUnknown || redirect.Target == "" {
					add(bashPlanForAction(command, accessLevel, level.Approvals.BashComplexFilesystem, "bash redirection cannot be resolved statically", "bash-access:complex"))
					continue
				}
				mode := ReadAccess
				if kind == redirectAccessWrite {
					mode = WriteAccess
				}
				// The invoking shell opens redirects before a wrapper changes cwd.
				pathReview(mode, resolveAgainstCwd(redirect.Target, candidateCwd))
			}
			if !x.Connector && len(x.Argv) > 0 && !x.Opaque && !x.Uncertain {
				effects := shellanalysis.Operands(commandFamily(x.Argv[0]), x.Argv[1:])
				for _, file := range effects.Files {
					mode := ReadAccess
					if file.Write {
						mode = WriteAccess
					}
					raw := resolveAgainstCwd(file.Path, x.Cwd)
					if session.AgentHasRuntimeSandbox && strings.ContainsAny(raw, "*?[") {
						add(bashPlan(command, accessLevel, DecisionBlock, "container globs require explicit paths until guest expansion is available", "bash-access:glob", raw))
						continue
					}
					paths, err := expandOperandPaths(raw)
					if err != nil {
						add(bashPlan(command, accessLevel, DecisionBlock, err.Error(), "bash-access:glob", raw))
						continue
					}
					for _, p := range paths {
						pathReview(mode, p)
						if file.Recursive {
							subtree := BuildSubtreePlan(cfg, session, mode, p)
							if subtree.Blocked() {
								add(bashPlan(command, accessLevel, DecisionBlock, subtree.Reason, "bash-access:subtree", p))
							}
						}
					}
				}
				if effects.ExecutesCode || effects.Unknown {
					add(executionFingerprint(opaqueBashPlan(command, accessLevel, level.Approvals.BashOpaqueCommand, x.Identity, x.Cwd), x, variables, environment))
				}
				if effects.RemoteMutation && accessLevel != AccessLevelFullAccess {
					add(bashPlan(command, accessLevel, DecisionRequiresApproval, "command modifies a remote resource", "bash-access:remote", command))
				}
			}
			if !x.Connector && x.Opaque && !x.Uncertain {
				// Opaque code is separately approved, but visible arguments still
				// cannot silently acquire outside write access in auto_approve.
				for _, arg := range opaquePathArguments(x) {
					target := resolveAgainstCwd(arg, x.Cwd)
					pathReview(WriteAccess, target)
					if p := BuildSubtreePlan(cfg, session, WriteAccess, target); p.Blocked() {
						add(bashPlan(command, accessLevel, DecisionBlock, p.Reason, "bash-access:subtree", target))
					}
				}
			}
			// Keep both success and failure branches. This deliberately over-approximates
			// conditionals/pipelines rather than authorizing a target under the old cwd.
			if len(x.Argv) > 0 && commandFamily(x.Argv[0]) == "cd" && len(parsed.Commands) > 1 {
				if len(x.Argv) == 2 && !strings.HasPrefix(x.Argv[1], "-") && !containsUnresolvedPlaceholder(x.Argv[1]) && len(possibleCwds) < 8 && variables["CDPATH"] == "" {
					possibleCwds = append(possibleCwds, resolveAgainstCwd(x.Argv[1], x.Cwd))
				} else {
					add(bashPlanForAction(command, accessLevel, level.Approvals.BashComplexFilesystem, "compound command changes cwd dynamically", "bash-access:complex"))
				}
			}
		}
		onlyConnectors = onlyConnectors && allConnector && len(cmd.EnvVars) == 0 && len(cmd.Redirects) == 0
		if allConnector {
			connectorWords = append(connectorWords, cmd.Words...)
		}
	}
	result := combineBashPlans(command, accessLevel, plans)
	if len(connectorWords) > 0 {
		result.HasConnector = true
		result.ConnectorOnly = onlyConnectors
		result.ReviewCommand = connectorReviewCommand(command, connectorWords)
		if result.Decision == DecisionAllow {
			result.RuleKey = "bash-access:connector"
			result.Reason = "mounted connector CLI"
		}
	}
	return result
}

func opaquePathArguments(x BashExecution) []string {
	var paths []string
	rawScript := ""
	if x.TrustedInterpreter && len(x.Argv) > 1 {
		rawScript = interpreterScript(commandFamily(x.Argv[0]), x.Argv[1:])
	}
	skipNext := false
	for _, arg := range x.Argv[1:] {
		if skipNext {
			skipNext = false
			continue
		}
		if arg == "-c" || arg == "-e" || arg == "--eval" || arg == "-m" {
			skipNext = true
			continue
		}
		if arg == x.Script || arg == rawScript || arg == "-" {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			_, value, ok := strings.Cut(arg, "=")
			if !ok {
				continue
			}
			arg = value
		}
		if strings.Contains(arg, "://") || arg == "" {
			continue
		}
		if strings.ContainsAny(arg, "/\\") || strings.HasPrefix(arg, "~") {
			paths = append(paths, arg)
		} else if _, err := os.Lstat(resolveAgainstCwd(arg, x.Cwd)); err == nil {
			paths = append(paths, arg)
		}
	}
	return paths
}

func skillExecutionMatches(session QuerySession, ctx *ExecutionContext, target string, env *BashEnvironment) bool {
	if session.SkillScripts == nil {
		return false
	}
	if !session.AgentHasRuntimeSandbox {
		host, err := ResolveSessionPath(session, target)
		return err == nil && session.SkillScripts.Matches(ctx.ScriptOwner(), host, "", false)
	}
	if env == nil || env.Canonical == nil || env.Inspect == nil {
		return false
	}
	canonical, err := env.Canonical(target, "/")
	if err != nil {
		return false
	}
	host, ok := session.SkillScripts.HostPath(canonical)
	if !ok {
		return false
	}
	_, hash, err := env.Inspect(canonical)
	return err == nil && session.SkillScripts.Matches(ctx.ScriptOwner(), host, hash, true)
}

func decisionPriority(d Decision) int {
	switch d {
	case DecisionBlock:
		return 3
	case DecisionRequiresApproval:
		return 2
	case DecisionAutoApproved:
		return 1
	}
	return 0
}

func combineBashPlans(command, level string, plans []BashPlan) BashPlan {
	if len(plans) == 0 {
		return bashPlan(command, level, DecisionAllow, "", "", command)
	}
	unique := []BashPlan{}
	seen := map[string]bool{}
	for _, p := range plans {
		key := p.RuleKey + "\x00" + p.Fingerprint
		if !seen[key] {
			unique = append(unique, p)
			seen[key] = true
		}
	}
	if len(unique) == 1 {
		return unique[0]
	}
	chosen := unique[0]
	var reasons, keys []string
	for _, p := range unique {
		if decisionPriority(p.Decision) > decisionPriority(chosen.Decision) {
			chosen = p
		}
		if p.Reason != "" {
			reasons = append(reasons, p.Reason)
		}
		keys = append(keys, p.RuleKey+":"+p.Fingerprint+":"+string(p.Decision))
	}
	if chosen.Allowed() && !chosen.AutoApproved() {
		return chosen
	}
	sum := sha256.Sum256([]byte(strings.Join(keys, "\x00")))
	return BashPlan{Decision: chosen.Decision, Reason: strings.Join(reasons, "; "), RuleKey: chosen.RuleKey + ":combined:" + hex.EncodeToString(sum[:8]), Fingerprint: hex.EncodeToString(sum[:]), CommandText: command, AccessLevel: level, Requirements: unique}
}

func hasExactApproval(ctx *ExecutionContext, p BashPlan) bool {
	return ctx != nil && ctx.AccessPolicyApprovals[p.Fingerprint] > 0
}

// ApprovalRules returns only requirements the user is being asked to approve.
func ApprovalRules(p BashPlan) []string {
	if p.Blocked() {
		return nil
	}
	if len(p.Requirements) == 0 {
		if p.RequiresApproval() {
			return []string{p.RuleKey}
		}
		return nil
	}
	var rules []string
	seen := map[string]bool{}
	for _, leaf := range p.Requirements {
		for _, rule := range ApprovalRules(leaf) {
			if !seen[rule] {
				rules = append(rules, rule)
				seen[rule] = true
			}
		}
	}
	return rules
}

// PendingBashPlan removes approved leaves but never a hard block. Re-evaluation
// uses the current file contents/environment rather than a cached allow result.
func PendingBashPlan(ctx *ExecutionContext, p BashPlan) BashPlan {
	if p.Blocked() || !p.RequiresApproval() || hasExactApproval(ctx, p) {
		return p
	}
	if len(p.Requirements) == 0 {
		return p
	}
	var leaves []BashPlan
	for _, leaf := range p.Requirements {
		if leaf.RequiresApproval() && HasApproval(ctx, leaf) {
			continue
		}
		leaves = append(leaves, leaf)
	}
	result := combineBashPlans(p.CommandText, p.AccessLevel, leaves)
	result.HasConnector, result.ReviewCommand, result.ConnectorOnly = p.HasConnector, p.ReviewCommand, p.ConnectorOnly
	return result
}

func BashPlanMetadata(p BashPlan) map[string]any {
	out := map[string]any{"decision": string(p.Decision), "accessLevel": p.AccessLevel, "reason": p.Reason, "ruleKey": p.RuleKey}
	if p.ScopeKind != "" {
		out["scopeKind"], out["scope"] = p.ScopeKind, p.Scope
	}
	if p.ContentSHA256 != "" {
		out["contentSHA256"] = p.ContentSHA256
	}
	if len(p.Requirements) > 0 {
		items := []any{}
		for _, leaf := range p.Requirements {
			items = append(items, BashPlanMetadata(leaf))
		}
		out["requirements"] = items
	}
	return out
}

func BashApprovalSource(ctx *ExecutionContext, p BashPlan) string {
	if pending := PendingBashPlan(ctx, p); pending.RequiresApproval() {
		p = pending
	}
	if !p.RequiresApproval() || !HasApproval(ctx, p) {
		return ""
	}
	for _, rule := range ApprovalRules(p) {
		if !ctx.AccessPolicyRuleApprovals[rule] {
			return "exact"
		}
	}
	return "run_rule"
}
