package accesspolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"

	"agent-platform/internal/bashast"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/shellanalysis"
	shellsyntax "agent-platform/internal/shellreview/syntax"
)

// maxShellScriptDepth bounds recursive analysis of literal `bash -c` scripts.
const maxShellScriptDepth = 3

func reviewBashExecution(cfg config.AccessPolicyConfig, session QuerySession, command, cwd string, variables map[string]string, environment *BashEnvironment, contexts ...*ExecutionContext) BashPlan {
	unknownDialect := environment != nil && environment.Launch != nil && !environment.Launch.SyntaxSupported
	if reason := rawTextRejection(command, unknownDialect); reason != "" {
		p := bashPlan(command, sessionAccessLevel(session), DecisionBlock, reason, "bash-access:text", command)
		p.UnknownDialect = unknownDialect
		return p
	}
	var execCtx *ExecutionContext
	if len(contexts) > 0 {
		execCtx = contexts[0]
	}
	plan := reviewBashScript(cfg, session, command, cwd, variables, environment, execCtx, 0)
	if environment != nil && environment.Launch != nil && !plan.ConnectorOnly {
		plan = bindBashLaunch(plan, *environment.Launch)
	}
	return plan
}

// rawTextRejection judges hidden characters by the grammar that executes the
// text: Bash (including direct connector argv) ignores quoted data, while an
// unsupported grammar cannot tell quoting apart, so every character counts.
func rawTextRejection(command string, unknownDialect bool) string {
	if unknownDialect {
		return shellsyntax.CheckAll(command)
	}
	return bashast.CheckRawText(command)
}

// DefaultBashCwd is the working directory used when a Bash call names none.
// A read-only Workspace starts programs in the current Chat instead.
func DefaultBashCwd(session QuerySession) string {
	if sessionWorkspaceEditingDisabled(session) {
		return "@chat"
	}
	return "@workspace"
}

func reviewBashScript(cfg config.AccessPolicyConfig, session QuerySession, command, cwd string, variables map[string]string, environment *BashEnvironment, execCtx *ExecutionContext, depth int) BashPlan {
	accessLevel := sessionAccessLevel(session)
	level := EffectiveLevel(cfg, accessLevel)
	command = strings.TrimSpace(command)
	if command == "" {
		return BashPlan{Decision: DecisionAllow}
	}
	if strings.TrimSpace(cwd) == "" {
		cwd = DefaultBashCwd(session)
	}
	workingDir, err := ResolveSessionPath(session, cwd)
	if err != nil {
		return bashPlan(command, accessLevel, DecisionBlock, err.Error(), "bash-access:cwd", cwd)
	}
	workingDir, err = NormalizePath(workingDir)
	if err != nil {
		return bashPlan(command, accessLevel, DecisionBlock, err.Error(), "bash-access:cwd", cwd)
	}
	var plans []BashPlan
	sshAgent := false
	add := func(p BashPlan) {
		if p.Decision != DecisionAllow || p.RuleKey == "bash-access:authored-script" || p.RuleKey == "bash-access:skill-script" {
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
		reason := "bash path is outside allowed roots: " + p.Path
		switch {
		case p.Blocked():
			reason = "bash path blocked: " + p.Reason + ": " + p.Path
		case p.RequiresApproval() && p.Reason != outsideRootsReason(mode):
			reason = "bash path requires approval: " + p.Reason + ": " + p.Path
		}
		add(reviewBashPathPlan(command, accessLevel, level, mode, p, reason))
	}
	opaquePlan := func(x BashExecution) BashPlan {
		return executionFingerprint(opaqueBashPlan(command, accessLevel, level.Approvals.BashOpaqueCommand, x.Identity, x.Cwd), x, variables, environment)
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
	editingDisabled := sessionWorkspaceEditingDisabled(session)
	if environment != nil && environment.Launch != nil && !environment.Launch.SyntaxSupported &&
		DirectConnectorInvocation(session, command, workingDir, variables) == nil {
		pathReview(ReadAccess, workingDir)
		if editingDisabled && PathInSessionWorkspace(session, workingDir) {
			add(bashPlan(command, accessLevel, DecisionBlock, workspaceReadOnlyExecutionReason, "bash-access:workspace-readonly", workingDir))
		}
		// Missing grammar support is always an exact human decision in the two
		// restricted levels; auto_approve must not hide unknown remote/destructive effects.
		decision := decisionForAction(level.Approvals.BashComplexFilesystem)
		if decision != DecisionBlock && accessLevel != AccessLevelFullAccess {
			decision = DecisionRequiresApproval
		}
		add(bashPlan(command, accessLevel, decision,
			fmt.Sprintf("shell syntax is not analyzed (%s); executable=%q; startup=%q: %s", environment.Launch.Dialect, environment.Launch.Executable, environment.Launch.Template, environment.Launch.Reason),
			"bash-access:complex:dialect", command))
		plan := combineBashPlans(command, accessLevel, plans)
		plan.UnknownDialect = true
		return plan
	}
	parsed := bashast.ParseForSecurityWithKnownVariables(command, variables)
	if len(session.ConnectorCLIEntries) > 0 {
		parsed = bashast.ParseForExecution(command, variables)
	}
	if parsed.Kind != bashast.Simple {
		pathReview(ReadAccess, workingDir)
		if editingDisabled && PathInSessionWorkspace(session, workingDir) {
			add(bashPlan(command, accessLevel, DecisionBlock, workspaceReadOnlyExecutionReason, "bash-access:workspace-readonly", workingDir))
		}
		add(bashPlanForAction(command, accessLevel, level.Approvals.BashComplexFilesystem, "bash command is too complex for access-policy path analysis", "bash-access:complex"))
		return combineBashPlans(command, accessLevel, plans)
	}
	var connectorWords []bashast.WordSpan
	connectorIDs := map[string]bool{}
	onlyConnectors := len(parsed.Commands) > 0
	if len(parsed.Commands) == 0 {
		pathReview(ReadAccess, workingDir)
	}
	possibleCwds := []string{workingDir}
	var call *ConnectorCall
	for _, cmd := range parsed.Commands {
		allConnector := len(cmd.Words) > 0
		for _, candidateCwd := range append([]string(nil), possibleCwds...) {
			x := analyzeBashExecution(session, cmd, candidateCwd, variables, environment)
			if depth == 0 && len(parsed.Commands) == 1 {
				call = connectorCall(command, variables, x)
			}
			if x.BlockReason != "" {
				add(bashPlan(command, accessLevel, DecisionBlock, x.BlockReason, "bash-access:temp-escape", cmd.Text))
			}
			allConnector = allConnector && x.Connector
			if x.ConnectorID != "" {
				connectorIDs[x.ConnectorID] = true
			}
			// executes marks code whose file effects are not described by the analysis.
			executes := x.Connector
			if !x.Connector {
				pathReview(ReadAccess, candidateCwd)
				pathReview(ReadAccess, x.Cwd)
				if x.Program != "" {
					pathReview(ReadAccess, x.Program)
				}
				if x.Script != "" {
					pathReview(ReadAccess, resolveAgainstCwd(x.Script, x.Cwd))
				}
			}
			switch {
			case x.Connector, len(x.Argv) == 0:
			case x.Uncertain:
				executes = true
				add(bashPlanForAction(command, accessLevel, level.Approvals.BashComplexFilesystem, "bash execution wrapper or target cannot be resolved statically", "bash-access:complex"))
			case x.Opaque:
				if script, ok := shellanalysis.ShellScript(x.Argv); ok && x.TrustedInterpreter && depth < maxShellScriptDepth {
					// A literal `bash -c` script is analyzed like the outer command, so
					// wrappers cannot hide remote mutations or file effects.
					inner := reviewBashScript(cfg, session, script, x.Cwd, variables, environment, execCtx, depth+1)
					sshAgent = sshAgent || inner.UsesSSHAgent
					// A wrapper does not hide which connectors run.
					for _, id := range inner.ConnectorIDs {
						connectorIDs[id] = true
					}
					add(inner)
					break
				}
				executes = true
				if decisionForAction(level.Approvals.BashOpaqueCommand) == DecisionBlock {
					add(opaqueBashPlan(command, accessLevel, level.Approvals.BashOpaqueCommand, x.Identity, x.Cwd))
				}
				switch {
				case x.Script != "" && execCtx != nil && skillExecutionMatches(session, execCtx, resolveAgainstCwd(x.Script, x.Cwd), environment):
					add(bashPlan(command, accessLevel, DecisionAllow, "script matches this run's selected skill", "bash-access:skill-script", x.Script))
				case authoredScriptExempt(session, execCtx, x, environment):
					add(bashPlan(command, accessLevel, DecisionAllow, "script was written by this run in the current Chat directory", "bash-access:authored-script", resolveAgainstCwd(x.Script, x.Cwd)))
				default:
					add(opaquePlan(x))
				}
				// Opaque code is separately approved, but visible arguments still
				// cannot silently acquire outside write access in auto_approve.
				for _, arg := range opaquePathArguments(x) {
					target := resolveAgainstCwd(arg, x.Cwd)
					pathReview(WriteAccess, target)
					// A program handed a tree that contains platform secrets, other Chats or
					// readonly roots cannot be shown to stay out of them; approval must not
					// relax the hard protection.
					if p := BuildSubtreePlan(cfg, session, WriteAccess, target); p.Blocked() {
						add(bashPlan(command, accessLevel, DecisionBlock, p.Reason, "bash-access:subtree", target))
					}
				}
			default:
				effects := shellanalysis.Operands(commandFamily(x.Argv[0]), x.Argv[1:])
				repoDir := x.Cwd
				for i, file := range effects.Files {
					// curl/wget output options name a sink, just like shell > /dev/null.
					// Keep ordinary files, uploads, and destructive commands under policy.
					base := commandFamily(x.Argv[0])
					if !effects.Incomplete && (base == "curl" || base == "wget") && file.Write && file.Path == "/dev/null" && (session.AgentHasRuntimeSandbox || runtime.GOOS != "windows") {
						if p, err := BuildPathPlan(cfg, session, WriteAccess, file.Path); err == nil && !p.Blocked() {
							continue
						}
					}
					mode := ReadAccess
					if file.Write {
						mode = WriteAccess
					}
					raw := resolveAgainstCwd(file.Path, x.Cwd)
					if i == 0 && commandFamily(x.Argv[0]) == "git" && len(x.Argv) > 1 && strings.HasPrefix(x.Argv[1], "-C") {
						repoDir = raw
					}
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
				gitClean := effects.GitCheck != shellanalysis.GitCheckNone && !effects.ExecutesCode && gitExecutionClean(session, x, repoDir, effects.GitCheck, variables)
				if effects.ExecutesCode || effects.GitCheck != shellanalysis.GitCheckNone && !gitClean {
					executes = true
					add(opaquePlan(x))
				}
				if effects.RepoWrite {
					pathReview(WriteAccess, repoDir)
				}
				if effects.Destructive {
					add(actionPlan(command, accessLevel, level.Approvals.Destructive, "command discards data (recursive deletion or uncommitted changes)", "bash-access:destructive", x.Cwd))
				}
				if effects.RemoteMutation {
					add(bashPlanForAction(command, accessLevel, level.Approvals.RemoteMutation, "command modifies a remote resource", "bash-access:remote"))
				}
				sshAgent = sshAgent || effects.SSHAgent
			}
			if executes && editingDisabled && PathInSessionWorkspace(session, x.Cwd) {
				add(bashPlan(command, accessLevel, DecisionBlock, workspaceReadOnlyExecutionReason, "bash-access:workspace-readonly", x.Cwd))
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
	result.UsesSSHAgent = sshAgent
	for id := range connectorIDs {
		result.ConnectorIDs = append(result.ConnectorIDs, id)
	}
	sort.Strings(result.ConnectorIDs)
	result.ConnectorCall = call
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

const workspaceReadOnlyExecutionReason = "workspace editing is disabled: programs whose effects cannot be analyzed may not run inside the Workspace; use cwd @chat"

// actionPlan approves one exact invocation class; the run grant reuses only
// the same command text in the same working directory.
func actionPlan(command, accessLevel, action, reason, rule, cwd string) BashPlan {
	sum := sha256.Sum256([]byte(command + "\x00" + cwd))
	return bashPlan(command, accessLevel, decisionForAction(action), reason, rule+":"+hex.EncodeToString(sum[:8]), command+"\x00"+cwd)
}

// authoredScriptExempt accepts a script that this Run wrote through the managed
// file tools, located in the current Chat directory and started by a verified
// system interpreter (directly or through its shebang).
func authoredScriptExempt(session QuerySession, ctx *ExecutionContext, x BashExecution, env *BashEnvironment) bool {
	if ctx == nil || ctx.AuthoredScripts == nil || x.Script == "" || x.Wrapped {
		return false
	}
	if !x.TrustedInterpreter && !isInterpreter(x.Identity) {
		return false
	}
	target := resolveAgainstCwd(x.Script, x.Cwd)
	host, ok := executableHostPath(session, target)
	if !ok || !PathInSessionChat(session, host) {
		return false
	}
	if !ctx.AuthoredScripts.Matches(ctx.ScriptOwner(), host) {
		return false
	}
	if !session.AgentHasRuntimeSandbox {
		return true
	}
	if env == nil || env.Inspect == nil {
		return false
	}
	_, hash, err := env.Inspect(target)
	return err == nil && ctx.AuthoredScripts.MatchesHash(ctx.ScriptOwner(), host, hash)
}

// opaquePathArguments returns arguments that visibly name local paths: "." and
// "..", or words with a separator or ~. Bare words are not guessed from the
// filesystem; a module name is not a path merely because such a file exists.
// Programs with modeled grammars (jq, tar, ...) never reach this heuristic.
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
		if arg == "." || arg == ".." || strings.ContainsAny(arg, "/\\") || strings.HasPrefix(arg, "~") {
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
	result.ConnectorIDs = append([]string(nil), p.ConnectorIDs...)
	result.ConnectorCall = p.ConnectorCall
	result.UnknownDialect, result.LaunchFingerprint = p.UnknownDialect, p.LaunchFingerprint
	result.Launch = p.Launch
	return result
}

func BashPlanMetadata(p BashPlan) map[string]any {
	out := map[string]any{"decision": string(p.Decision), "accessLevel": p.AccessLevel, "reason": p.Reason, "ruleKey": p.RuleKey}
	if p.Launch != nil {
		out["shell"] = map[string]any{"executable": p.Launch.Executable, "template": append([]string(nil), p.Launch.Template...), "dialect": string(p.Launch.Dialect), "syntaxSupported": p.Launch.SyntaxSupported}
	}
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
