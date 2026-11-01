package shellreview

import (
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"

	. "agent-platform/internal/accesspolicy"
	"agent-platform/internal/commandtext"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/shellreview/commands"
	"agent-platform/internal/shellreview/identity"
	shellplan "agent-platform/internal/shellreview/plan"
	"agent-platform/internal/shellreview/resources"
	bashsyntax "agent-platform/internal/shellreview/syntax/bash"
	"agent-platform/internal/shellreview/types"
	"agent-platform/internal/shellreview/unwrap"
)

// maxShellScriptDepth bounds recursive analysis of literal `bash -c` scripts.
const maxShellScriptDepth = 3

func reviewBashExecution(cfg config.AccessPolicyConfig, session QuerySession, command, cwd string, variables map[string]string, environment *types.BashEnvironment, contexts ...*ExecutionContext) shellplan.BashPlan {
	unknownDialect := environment != nil && environment.Launch != nil && !environment.Launch.SyntaxSupported
	if reason := rawTextRejection(command, unknownDialect); reason != "" {
		p := shellplan.New(command, SessionAccessLevel(session), DecisionBlock, reason, "bash-access:text", command)
		p.UnknownDialect = unknownDialect
		return p
	}
	var execCtx *ExecutionContext
	if len(contexts) > 0 {
		execCtx = contexts[0]
	}
	plan := reviewBashScript(cfg, session, command, cwd, variables, environment, execCtx, 0)
	if environment != nil && environment.Launch != nil && !plan.ConnectorOnly {
		plan = shellplan.BindLaunch(plan, *environment.Launch)
	}
	return plan
}

// rawTextRejection judges hidden characters by the grammar that executes the
// text: Bash (including direct connector argv) ignores quoted data, while an
// unsupported grammar cannot tell quoting apart, so every character counts.
func rawTextRejection(command string, unknownDialect bool) string {
	if unknownDialect {
		return commandtext.CheckAll(command)
	}
	return bashsyntax.CheckRawText(command)
}

// DefaultBashCwd is the working directory used when a Bash call names none.
// A read-only Workspace starts programs in the current Chat instead.
func DefaultBashCwd(session QuerySession) string {
	if SessionWorkspaceEditingDisabled(session) {
		return "@chat"
	}
	return "@workspace"
}

func reviewBashScript(cfg config.AccessPolicyConfig, session QuerySession, command, cwd string, variables map[string]string, environment *types.BashEnvironment, execCtx *ExecutionContext, depth int) shellplan.BashPlan {
	accessLevel := SessionAccessLevel(session)
	level := EffectiveLevel(cfg, accessLevel)
	command = strings.TrimSpace(command)
	if command == "" {
		return shellplan.BashPlan{Decision: DecisionAllow}
	}
	if strings.TrimSpace(cwd) == "" {
		cwd = DefaultBashCwd(session)
	}
	workingDir, err := ResolveSessionPath(session, cwd)
	if err != nil {
		return shellplan.New(command, accessLevel, DecisionBlock, err.Error(), "bash-access:cwd", cwd)
	}
	workingDir, err = NormalizePath(workingDir)
	if err != nil {
		return shellplan.New(command, accessLevel, DecisionBlock, err.Error(), "bash-access:cwd", cwd)
	}
	var plans []shellplan.BashPlan
	sshAgent := false
	add := func(p shellplan.BashPlan) {
		if p.Decision != DecisionAllow || p.RuleKey == "bash-access:authored-script" || p.RuleKey == "bash-access:skill-script" {
			plans = append(plans, p)
		}
	}
	pathReview := func(mode AccessMode, raw string) {
		if containsUnresolvedPlaceholder(raw) {
			add(shellplan.ForAction(command, accessLevel, level.Approvals.BashComplexFilesystem, "bash path cannot be resolved statically", "bash-access:complex"))
			return
		}
		p, err := BuildPathPlan(cfg, session, mode, raw)
		if err != nil {
			add(shellplan.New(command, accessLevel, DecisionBlock, err.Error(), "bash-access:path-invalid", raw))
			return
		}
		reason := "bash path is outside allowed roots: " + p.Path
		switch {
		case p.Blocked():
			reason = "bash path blocked: " + p.Reason + ": " + p.Path
		case p.RequiresApproval() && p.Reason != OutsideRootsReason(mode):
			reason = "bash path requires approval: " + p.Reason + ": " + p.Path
		}
		add(shellplan.ReviewPath(command, accessLevel, level, mode, p, reason))
	}
	opaquePlan := func(x types.BashExecution) shellplan.BashPlan {
		p := shellplan.Opaque(command, accessLevel, level.Approvals.BashOpaqueCommand, x.Identity, x.Cwd)
		version := resources.ExecutionFingerprint(session.AgentHasRuntimeSandbox, resources.ExecutionVersion{Fingerprint: p.Fingerprint, RuleKey: p.RuleKey, AccessLevel: p.AccessLevel}, x, variables, environment)
		p.Fingerprint, p.RuleKey = version.Fingerprint, version.RuleKey
		p.Scope, p.ScopeKind, p.ContentSHA256 = version.Scope, version.ScopeKind, version.ContentSHA256
		return p
	}
	if session.AgentHasRuntimeSandbox && environment != nil && environment.Directory != nil {
		if canonical, err := environment.Directory(workingDir); err == nil {
			workingDir = canonical
		} else if errors.Is(err, types.ErrBashTemporaryEscape) {
			add(shellplan.New(command, accessLevel, DecisionBlock, err.Error(), "bash-access:temp-escape", workingDir))
		} else {
			add(shellplan.ForAction(command, accessLevel, level.Approvals.BashComplexFilesystem, "container working directory cannot be resolved", "bash-access:complex"))
		}
	}
	editingDisabled := SessionWorkspaceEditingDisabled(session)
	if environment != nil && environment.Launch != nil && !environment.Launch.SyntaxSupported &&
		DirectConnectorInvocation(session, command, workingDir, variables) == nil {
		pathReview(ReadAccess, workingDir)
		if editingDisabled && PathInSessionWorkspace(session, workingDir) {
			add(shellplan.New(command, accessLevel, DecisionBlock, workspaceReadOnlyExecutionReason, "bash-access:workspace-readonly", workingDir))
		}
		// Missing grammar support is always an exact human decision in the two
		// restricted levels; auto_approve must not hide unknown remote/destructive effects.
		decision := DecisionForAction(level.Approvals.BashComplexFilesystem)
		if decision != DecisionBlock && accessLevel != AccessLevelFullAccess {
			decision = DecisionRequiresApproval
		}
		add(shellplan.New(command, accessLevel, decision,
			fmt.Sprintf("shell syntax is not analyzed (%s); executable=%q; startup=%q: %s", environment.Launch.Dialect, environment.Launch.Executable, environment.Launch.Template, environment.Launch.Reason),
			"bash-access:complex:dialect", command))
		plan := shellplan.Combine(command, accessLevel, plans)
		plan.UnknownDialect = true
		return plan
	}
	parsed := bashsyntax.ParseForSecurityWithKnownVariables(command, variables)
	if len(session.ConnectorCLIEntries) > 0 {
		parsed = bashsyntax.ParseForExecution(command, variables)
	}
	if parsed.Kind != bashsyntax.Simple {
		pathReview(ReadAccess, workingDir)
		if editingDisabled && PathInSessionWorkspace(session, workingDir) {
			add(shellplan.New(command, accessLevel, DecisionBlock, workspaceReadOnlyExecutionReason, "bash-access:workspace-readonly", workingDir))
		}
		add(shellplan.ForAction(command, accessLevel, level.Approvals.BashComplexFilesystem, "bash command is too complex for access-policy path analysis", "bash-access:complex"))
		return shellplan.Combine(command, accessLevel, plans)
	}
	var connectorWords []types.WordSpan
	connectorIDs := map[string]bool{}
	onlyConnectors := len(parsed.Commands) > 0
	if len(parsed.Commands) == 0 {
		pathReview(ReadAccess, workingDir)
	}
	possibleCwds := []string{workingDir}
	var call *types.ConnectorCall
	for _, cmd := range parsed.Commands {
		allConnector := len(cmd.Words) > 0
		for _, candidateCwd := range append([]string(nil), possibleCwds...) {
			x := analyzeBashExecution(session, cmd, candidateCwd, variables, environment)
			if depth == 0 && len(parsed.Commands) == 1 {
				call = connectorCall(command, variables, x)
			}
			if x.BlockReason != "" {
				add(shellplan.New(command, accessLevel, DecisionBlock, x.BlockReason, "bash-access:temp-escape", cmd.Text))
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
					pathReview(ReadAccess, ResolveAgainstCwd(x.Script, x.Cwd))
				}
			}
			switch {
			case x.Connector, len(x.Argv) == 0:
			case x.Uncertain:
				executes = true
				add(shellplan.ForAction(command, accessLevel, level.Approvals.BashComplexFilesystem, "bash execution wrapper or target cannot be resolved statically", "bash-access:complex"))
			case x.Opaque:
				if script, ok := unwrap.ShellScript(x.Argv); ok && x.TrustedInterpreter && depth < maxShellScriptDepth {
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
				if DecisionForAction(level.Approvals.BashOpaqueCommand) == DecisionBlock {
					add(shellplan.Opaque(command, accessLevel, level.Approvals.BashOpaqueCommand, x.Identity, x.Cwd))
				}
				switch {
				case x.Script != "" && execCtx != nil && skillExecutionMatches(session, execCtx, ResolveAgainstCwd(x.Script, x.Cwd), environment):
					add(shellplan.New(command, accessLevel, DecisionAllow, "script matches this run's selected skill", "bash-access:skill-script", x.Script))
				case authoredScriptExempt(session, execCtx, x, environment):
					add(shellplan.New(command, accessLevel, DecisionAllow, "script was written by this run in the current Chat directory", "bash-access:authored-script", ResolveAgainstCwd(x.Script, x.Cwd)))
				default:
					add(opaquePlan(x))
				}
				// Opaque code is separately approved, but visible arguments still
				// cannot silently acquire outside write access in auto_approve.
				for _, arg := range opaquePathArguments(x) {
					target := ResolveAgainstCwd(arg, x.Cwd)
					pathReview(WriteAccess, target)
					// A program handed a tree that contains platform secrets, other Chats or
					// readonly roots cannot be shown to stay out of them; approval must not
					// relax the hard protection.
					if p := BuildSubtreePlan(cfg, session, WriteAccess, target); p.Blocked() {
						add(shellplan.New(command, accessLevel, DecisionBlock, p.Reason, "bash-access:subtree", target))
					}
				}
			default:
				effects := commands.Operands(identity.CommandFamily(x.Argv[0]), x.Argv[1:])
				for _, name := range commands.ConfigVariables(identity.CommandFamily(x.Argv[0]), x.Argv[1:]) {
					if x.Env[name] != "" {
						effects.ExecutesCode = true
					}
				}
				repoDir := x.Cwd
				for i, file := range effects.Files {
					// curl/wget output options name a sink, just like shell > /dev/null.
					// Keep ordinary files, uploads, and destructive commands under policy.
					base := identity.CommandFamily(x.Argv[0])
					if !effects.Incomplete && (base == "curl" || base == "wget") && file.Write && file.Path == "/dev/null" && (session.AgentHasRuntimeSandbox || runtime.GOOS != "windows") {
						if p, err := BuildPathPlan(cfg, session, WriteAccess, file.Path); err == nil && !p.Blocked() {
							continue
						}
					}
					mode := ReadAccess
					if file.Write {
						mode = WriteAccess
					}
					raw := ResolveAgainstCwd(file.Path, x.Cwd)
					if i == 0 && identity.CommandFamily(x.Argv[0]) == "git" && len(x.Argv) > 1 && strings.HasPrefix(x.Argv[1], "-C") {
						repoDir = raw
					}
					if session.AgentHasRuntimeSandbox && strings.ContainsAny(raw, "*?[") {
						add(shellplan.New(command, accessLevel, DecisionBlock, "container globs require explicit paths until guest expansion is available", "bash-access:glob", raw))
						continue
					}
					paths, err := resources.ExpandOperandPaths(raw)
					if err != nil {
						add(shellplan.New(command, accessLevel, DecisionBlock, err.Error(), "bash-access:glob", raw))
						continue
					}
					for _, p := range paths {
						pathReview(mode, p)
						if file.Recursive {
							subtree := BuildSubtreePlan(cfg, session, mode, p)
							if subtree.Blocked() {
								add(shellplan.New(command, accessLevel, DecisionBlock, subtree.Reason, "bash-access:subtree", p))
							}
						}
					}
				}
				gitClean := effects.GitCheck != types.GitCheckNone && !effects.ExecutesCode && resources.GitExecutionClean(session.AgentHasRuntimeSandbox, x, repoDir, effects.GitCheck, variables)
				if effects.ExecutesCode || effects.GitCheck != types.GitCheckNone && !gitClean {
					executes = true
					add(opaquePlan(x))
				} else if effects.Incomplete {
					// Unrecognized options may add outputs or behavior beyond the visible
					// operands; escalated writes alone would allow them inside write roots.
					add(shellplan.ForAction(command, accessLevel, level.Approvals.BashComplexFilesystem, "command options are not fully recognized", "bash-access:complex"))
				}
				if effects.RepoWrite {
					pathReview(WriteAccess, repoDir)
				}
				if effects.Destructive {
					add(shellplan.ForInvocation(command, accessLevel, level.Approvals.Destructive, "command discards data (recursive deletion or uncommitted changes)", "bash-access:destructive", x.Cwd))
				}
				if effects.RemoteMutation {
					add(shellplan.ForAction(command, accessLevel, level.Approvals.RemoteMutation, "command modifies a remote resource", "bash-access:remote"))
				}
				sshAgent = sshAgent || effects.SSHAgent
			}
			if executes && editingDisabled && PathInSessionWorkspace(session, x.Cwd) {
				add(shellplan.New(command, accessLevel, DecisionBlock, workspaceReadOnlyExecutionReason, "bash-access:workspace-readonly", x.Cwd))
			}
			for _, redirect := range cmd.Redirects {
				kind := resources.ClassifyRedirectAccess(redirect)
				if kind == resources.RedirectAccessNeutral {
					continue
				}
				if kind == resources.RedirectAccessUnknown || redirect.Target == "" {
					add(shellplan.ForAction(command, accessLevel, level.Approvals.BashComplexFilesystem, "bash redirection cannot be resolved statically", "bash-access:complex"))
					continue
				}
				mode := ReadAccess
				if kind == resources.RedirectAccessWrite {
					mode = WriteAccess
				}
				// The invoking shell opens redirects before a wrapper changes cwd.
				pathReview(mode, ResolveAgainstCwd(redirect.Target, candidateCwd))
			}
			// Keep both success and failure branches. This deliberately over-approximates
			// conditionals/pipelines rather than authorizing a target under the old cwd.
			if len(x.Argv) > 0 && identity.CommandFamily(x.Argv[0]) == "cd" && len(parsed.Commands) > 1 {
				if len(x.Argv) == 2 && !strings.HasPrefix(x.Argv[1], "-") && !containsUnresolvedPlaceholder(x.Argv[1]) && len(possibleCwds) < 8 && variables["CDPATH"] == "" {
					possibleCwds = append(possibleCwds, ResolveAgainstCwd(x.Argv[1], x.Cwd))
				} else {
					add(shellplan.ForAction(command, accessLevel, level.Approvals.BashComplexFilesystem, "compound command changes cwd dynamically", "bash-access:complex"))
				}
			}
		}
		onlyConnectors = onlyConnectors && allConnector && len(cmd.EnvVars) == 0 && len(cmd.Redirects) == 0
		if allConnector {
			connectorWords = append(connectorWords, cmd.Words...)
		}
	}
	result := shellplan.Combine(command, accessLevel, plans)
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

// authoredScriptExempt accepts a script that this Run wrote through the managed
// file tools, located in the current Chat directory and started by a verified
// system interpreter (directly or through its shebang).
func authoredScriptExempt(session QuerySession, ctx *ExecutionContext, x types.BashExecution, env *types.BashEnvironment) bool {
	if ctx == nil || ctx.AuthoredScripts == nil || x.Script == "" || x.Wrapped {
		return false
	}
	if !x.TrustedInterpreter && !identity.IsInterpreter(x.Identity) {
		return false
	}
	target := ResolveAgainstCwd(x.Script, x.Cwd)
	host, ok := identity.ExecutableHostPath(session, target)
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
func opaquePathArguments(x types.BashExecution) []string {
	var paths []string
	rawScript := ""
	if x.TrustedInterpreter && len(x.Argv) > 1 {
		rawScript = identity.InterpreterScript(identity.CommandFamily(x.Argv[0]), x.Argv[1:])
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

func skillExecutionMatches(session QuerySession, ctx *ExecutionContext, target string, env *types.BashEnvironment) bool {
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
