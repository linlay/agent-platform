package shellreview

import (
	"errors"
	"sort"
	"strings"

	. "agent-platform/internal/accesspolicy"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/shellreview/commands"
	"agent-platform/internal/shellreview/dialect"
	"agent-platform/internal/shellreview/identity"
	shellplan "agent-platform/internal/shellreview/plan"
	"agent-platform/internal/shellreview/policy"
	"agent-platform/internal/shellreview/resources"
	bashsyntax "agent-platform/internal/shellreview/syntax/bash"
	"agent-platform/internal/shellreview/types"
	"agent-platform/internal/shellreview/unwrap"
)

const maxShellScriptDepth = 3

func ReviewBashCommand(cfg config.AccessPolicyConfig, session QuerySession, command string, cwd string, variables map[string]string, contexts ...*ExecutionContext) shellplan.BashPlan {
	return reviewBashExecution(cfg, session, command, cwd, variables, nil, contexts...)
}

func ReviewBashCommandInEnvironment(cfg config.AccessPolicyConfig, session QuerySession, command, cwd string, variables map[string]string, environment *types.BashEnvironment, ctx *ExecutionContext) shellplan.BashPlan {
	return reviewBashExecution(cfg, session, command, cwd, variables, environment, ctx)
}

func reviewBashExecution(cfg config.AccessPolicyConfig, session QuerySession, command, cwd string, variables map[string]string, environment *types.BashEnvironment, contexts ...*ExecutionContext) shellplan.BashPlan {
	unknownDialect := environment != nil && environment.Launch != nil && !environment.Launch.SyntaxSupported
	if reason := rawTextRejection(command, unknownDialect); reason != "" {
		p := policy.New(cfg, session, command).Block(reason, "bash-access:text", command)
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

func reviewBashScript(cfg config.AccessPolicyConfig, session QuerySession, command, cwd string, variables map[string]string, environment *types.BashEnvironment, execCtx *ExecutionContext, depth int) shellplan.BashPlan {
	accessLevel := SessionAccessLevel(session)
	command = strings.TrimSpace(command)
	reviewer := policy.New(cfg, session, command)
	if command == "" {
		return reviewer.Allow()
	}
	if strings.TrimSpace(cwd) == "" {
		cwd = DefaultBashCwd(session)
	}
	workingDir, err := ResolveSessionPath(session, cwd)
	if err != nil {
		return reviewer.Block(err.Error(), "bash-access:cwd", cwd)
	}
	workingDir, err = NormalizePath(workingDir)
	if err != nil {
		return reviewer.Block(err.Error(), "bash-access:cwd", cwd)
	}
	var plans []shellplan.BashPlan
	sshAgent := false
	add := func(p shellplan.BashPlan) {
		if policy.KeepRequirement(p) {
			plans = append(plans, p)
		}
	}
	pathReview := func(mode AccessMode, raw string) {
		add(reviewer.Path(mode, raw, containsUnresolvedPlaceholder(raw)))
	}
	deviceInspector := resources.NewDeviceInspector(session.AgentHasRuntimeSandbox, environment)
	resourcePath := func(mode AccessMode, raw string, op types.Op, target types.Target, complete bool) {
		pathPlan := reviewer.Path(mode, raw, containsUnresolvedPlaceholder(raw))
		if pathPlan.Blocked() {
			add(pathPlan)
			return
		}
		result := deviceInspector.NullOffset(op, target, complete)
		for _, requirement := range reviewer.ResourceRequirements(pathPlan, result, raw) {
			add(requirement)
		}
	}
	opaquePlan := func(x types.BashExecution) shellplan.BashPlan {
		p := reviewer.Opaque(x)
		version := resources.ExecutionFingerprint(session.AgentHasRuntimeSandbox, resources.ExecutionVersion{Fingerprint: p.Fingerprint, RuleKey: p.RuleKey, AccessLevel: p.AccessLevel}, x, variables, environment)
		p.Fingerprint, p.RuleKey = version.Fingerprint, version.RuleKey
		p.Scope, p.ScopeKind, p.ContentSHA256 = version.Scope, version.ScopeKind, version.ContentSHA256
		return p
	}
	if session.AgentHasRuntimeSandbox && environment != nil && environment.Directory != nil {
		if canonical, err := environment.Directory(workingDir); err == nil {
			workingDir = canonical
		} else if errors.Is(err, types.ErrBashTemporaryEscape) {
			add(reviewer.Block(err.Error(), "bash-access:temp-escape", workingDir))
		} else {
			add(reviewer.Complex("container working directory cannot be resolved"))
		}
	}
	if environment != nil && environment.Launch != nil && !environment.Launch.SyntaxSupported &&
		DirectConnectorInvocation(session, command, workingDir, variables) == nil {
		pathReview(ReadAccess, workingDir)
		add(reviewer.WorkspaceExecution(workingDir))
		add(reviewer.UnknownDialect(*environment.Launch))
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
		add(reviewer.WorkspaceExecution(workingDir))
		add(reviewer.Complex("bash command is too complex for access-policy path analysis"))
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
				add(reviewer.Block(x.BlockReason, "bash-access:temp-escape", cmd.Text))
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
				add(reviewer.Complex("bash execution wrapper or target cannot be resolved statically"))
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
				if reviewer.OpaqueBlocked() {
					add(reviewer.Opaque(x))
				}
				switch {
				case x.Script != "" && execCtx != nil && identity.SkillExecutionMatches(session, execCtx, ResolveAgainstCwd(x.Script, x.Cwd), environment):
					add(reviewer.SkillScript(x.Script))
				case identity.AuthoredScriptExempt(session, execCtx, x, environment):
					add(reviewer.AuthoredScript(ResolveAgainstCwd(x.Script, x.Cwd)))
				default:
					add(opaquePlan(x))
				}
				// Opaque code is separately approved, but visible arguments still
				// cannot silently acquire outside write access in auto_approve.
				for _, arg := range resources.OpaquePathArguments(x) {
					target := ResolveAgainstCwd(arg, x.Cwd)
					pathReview(WriteAccess, target)
					// A program handed a tree that contains platform secrets, other Chats or
					// readonly roots cannot be shown to stay out of them; approval must not
					// relax the hard protection.
					add(reviewer.Subtree(WriteAccess, target))
				}
			default:
				effects := commands.Analyze(types.CommandInput{Name: identity.CommandFamily(x.Argv[0]), Args: x.Argv[1:], Words: x.Words[1:], Identity: x.CommandIdentity, Dialect: dialect.Bash})
				repoDir := x.Cwd
				gitCheck := types.GitCheckNone
				executesBehavior := effects.Has(types.Exec)
				for _, dep := range effects.Deps {
					switch dep.Kind {
					case types.Environment:
						executesBehavior = executesBehavior || x.Env[dep.Variable] != ""
					case types.GitConfig:
						gitCheck = types.GitCheck(dep.Check)
						if dep.Path != "" {
							repoDir = ResolveAgainstCwd(dep.Path, x.Cwd)
						}
					}
				}
				gitClean := gitCheck != types.GitCheckNone && !executesBehavior && resources.GitExecutionClean(session.AgentHasRuntimeSandbox, x, repoDir, gitCheck, variables)
				complete := len(effects.Unknowns) == 0 && !executesBehavior && (gitCheck == types.GitCheckNone || gitClean)
				for _, op := range effects.Ops {
					for _, target := range op.Targets {
						file := types.FileAccess{Path: target.Path, Write: op.WritesTarget(target), Recursive: op.Recursive}
						mode := ReadAccess
						if file.Write {
							mode = WriteAccess
						}
						if target.Placement != types.Exact && (file.Recursive || len(parsed.Commands) > 1) {
							add(reviewer.Subtree(mode, ResolveAgainstCwd(target.Path, x.Cwd)))
						}
						for _, raw := range resources.TargetPaths(op, target, x.Cwd, session.AgentHasRuntimeSandbox, environment) {
							if session.AgentHasRuntimeSandbox && strings.ContainsAny(raw, "*?[") {
								add(reviewer.Block("container globs require explicit paths until guest expansion is available", "bash-access:glob", raw))
								continue
							}
							paths, err := resources.ExpandOperandPaths(raw)
							if err != nil {
								add(reviewer.Block(err.Error(), "bash-access:glob", raw))
								continue
							}
							for _, p := range paths {
								resourcePath(mode, p, op, target, complete)
								if file.Write && op.ReadsTarget(target) {
									pathReview(ReadAccess, p)
									if file.Recursive {
										add(reviewer.Subtree(ReadAccess, p))
									}
								}
								if file.Recursive || file.Write && effects.Unknown(types.UnknownOption) || target.Placement == types.FileOrDirectory && len(parsed.Commands) > 1 {
									add(reviewer.Subtree(mode, p))
								}
							}
						}
					}
				}
				if executesBehavior || gitCheck != types.GitCheckNone && !gitClean {
					executes = true
					add(opaquePlan(x))
				} else if effects.Unknown(types.UnknownOption) {
					// Unrecognized options may add outputs or behavior beyond the visible
					// operands; escalated writes alone would allow them inside write roots.
					add(reviewer.Complex("command options are not fully recognized"))
				}
				if effects.Has(types.RepoWrite) {
					pathReview(WriteAccess, repoDir)
					add(reviewer.Subtree(WriteAccess, repoDir))
				}
				if effects.Has(types.Discard) {
					add(reviewer.Destructive(x.Cwd))
				}
				if effects.Has(types.RemoteMutation) {
					add(reviewer.RemoteMutation())
				}
				sshAgent = sshAgent || effects.SSHAgent
			}
			if executes {
				add(reviewer.WorkspaceExecution(x.Cwd))
			}
			redirects := resources.RedirectEffects(cmd.Redirects)
			if len(redirects.Unknowns) > 0 {
				add(reviewer.Complex("bash redirection cannot be resolved statically"))
			}
			for _, op := range redirects.Ops {
				for _, target := range op.Targets {
					mode := ReadAccess
					if op.WritesTarget(target) {
						mode = WriteAccess
					}
					// The invoking shell opens redirections before wrappers change cwd.
					resourcePath(mode, ResolveAgainstCwd(target.Path, candidateCwd), op, target, true)
				}
			}
			// Keep both the success and failure cwd branches, as before.
			unresolved := len(x.Argv) > 1 && containsUnresolvedPlaceholder(x.Argv[1])
			family := ""
			if len(x.Argv) > 0 {
				family = identity.CommandFamily(x.Argv[0])
			}
			target, changed, unknown := commands.CwdChange(family, x.Argv, len(parsed.Commands), len(possibleCwds), unresolved, variables["CDPATH"])
			if changed {
				possibleCwds = append(possibleCwds, ResolveAgainstCwd(target, x.Cwd))
			}
			if unknown {
				add(reviewer.Complex("compound command changes cwd dynamically"))
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
		result = reviewer.Connector(result)
	}
	return result
}
