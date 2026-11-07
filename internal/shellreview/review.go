package shellreview

import (
	"errors"
	"slices"
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
	// This reviewer implements Bash only. A backend cannot opt another grammar
	// into Bash analysis by setting a generic supported flag.
	if environment != nil && environment.Launch != nil && environment.Launch.SyntaxSupported && environment.Launch.Dialect != dialect.Bash {
		copied := *environment
		launch := *environment.Launch
		launch.SyntaxSupported = false
		launch.Reason = "this reviewer has no verified parser profile for the declared shell dialect"
		copied.Launch = &launch
		environment = &copied
	}
	unknownDialect := environment != nil && environment.Launch != nil && !environment.Launch.SyntaxSupported
	if reason := rawTextRejection(command, unknownDialect); reason != "" {
		p := policy.New(cfg, session, command).Block(reason, "bash-access:text", command)
		p.UnknownDialect = unknownDialect
		p.Syntax = true
		return p
	}
	var execCtx *ExecutionContext
	if len(contexts) > 0 {
		execCtx = contexts[0]
	}
	history := &scriptReviewHistory{}
	plan := reviewBashScript(cfg, session, command, cwd, variables, environment, execCtx, 0, history)
	plan.FindExecReviewed = history.findExecs > 0 && history.findExecPending == 0
	plan = includeSyntaxRequirements(plan, session, command, cwd, variables, environment)
	if environment != nil && environment.Launch != nil && !plan.ConnectorOnly {
		plan = shellplan.BindLaunch(plan, *environment.Launch)
	}
	return plan
}

type scriptReviewHistory struct {
	mayMutate bool
	// beyondPaths records a requirement other than path review or a hard
	// block, whatever the access level decided for it.
	beyondPaths bool
	// findExecs counts reviewed commands whose words mention -exec, and
	// findExecPending those not shown read-only by reviewFindExec.
	findExecs, findExecPending int
}

// reviewFindExec reviews each -exec command with {} replaced by every search
// root. If none of them can write, run unanalyzed code or use unmodeled
// options, find reads nothing beyond its recursive root review: it returns the
// arguments without -exec and the inner plans, whose path requirements stay.
func reviewFindExec(cfg config.AccessPolicyConfig, session QuerySession, x types.BashExecution, environment *types.BashEnvironment, execCtx *ExecutionContext, depth int) ([]string, []types.SourceWord, []shellplan.BashPlan, bool) {
	args, words := x.Argv[1:], x.Words[1:]
	roots, execs, keep, ok := commands.FindExec(args)
	if !ok || len(execs) == 0 || len(words) != len(args) || depth >= maxShellScriptDepth {
		return nil, nil, nil, false
	}
	var inner []shellplan.BashPlan
	for _, argv := range execs {
		for _, root := range roots {
			quoted := make([]string, len(argv))
			for i, word := range argv {
				quoted[i] = "'" + strings.ReplaceAll(strings.ReplaceAll(word, "{}", root), "'", `'\''`) + "'"
			}
			history := &scriptReviewHistory{}
			p := reviewBashScript(cfg, session, strings.Join(quoted, " "), x.Cwd, x.Env, environment, execCtx, depth+1, history)
			if history.mayMutate || history.beyondPaths || history.findExecPending > 0 {
				return nil, nil, nil, false
			}
			inner = append(inner, p)
		}
	}
	rest, restWords := make([]string, len(keep)), make([]types.SourceWord, len(keep))
	for i, k := range keep {
		rest[i], restWords[i] = args[k], words[k]
	}
	return rest, restWords, inner, true
}

// walkSkip lets a bounded walk pass protected directories it never visits.
// Container mounts may place them elsewhere, so it is Host-only, and -path
// tests need the root exactly as the program received it.
func walkSkip(session QuerySession, target types.Target) func(string, string) bool {
	if target.Walk == nil || session.AgentHasRuntimeSandbox {
		return nil
	}
	typed := ""
	if target.Word.Literal {
		typed = target.Path
	}
	return func(root, protected string) bool {
		return resources.WalkSkips(*target.Walk, typed, root, protected)
	}
}

// pathRequirement reports a plan that only allows, reviews paths or blocks.
func pathRequirement(p shellplan.BashPlan) bool {
	if len(p.Requirements) > 0 {
		for _, r := range p.Requirements {
			if !pathRequirement(r) {
				return false
			}
		}
		return true
	}
	return p.Blocked() || p.RuleKey == "" || p.RuleKey == "bash-access" || strings.HasPrefix(p.RuleKey, "bash-access:path")
}

// loopGlobs expands for-loop globs on the Host at review time, as file operand
// globs are; a container has no guest expansion yet, so only its literal loop
// items are walked per value.
func loopGlobs(session QuerySession, cwd string) bashsyntax.GlobExpander {
	if session.AgentHasRuntimeSandbox {
		return func(string, []string) ([]string, bool) { return nil, false }
	}
	return func(pattern string, dirs []string) ([]string, bool) {
		var values []string
		for _, dir := range dirs {
			base := cwd
			if dir != "" {
				base = ResolveAgainstCwd(dir, cwd)
			}
			matches, err := resources.ExpandOperandPaths(ResolveAgainstCwd(pattern, base))
			if err != nil {
				return nil, false
			}
			values = append(values, matches...)
		}
		return values, true
	}
}

func reviewBashScript(cfg config.AccessPolicyConfig, session QuerySession, command, cwd string, variables map[string]string, environment *types.BashEnvironment, execCtx *ExecutionContext, depth int, history *scriptReviewHistory) shellplan.BashPlan {
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
	hardViolationSeen := false
	sshAgent := false
	add := func(p shellplan.BashPlan) {
		if p.Blocked() {
			hardViolationSeen = true
		}
		history.beyondPaths = history.beyondPaths || !pathRequirement(p)
		if policy.KeepRequirement(p) {
			plans = append(plans, p)
		}
	}
	pathReview := func(mode AccessMode, raw string) {
		add(reviewer.Path(mode, raw, containsUnresolvedPlaceholder(raw)))
	}
	dependencyInspector := resources.NewDependencyInspector(session.AgentHasRuntimeSandbox, environment, func(path string) (resources.DependencyPermission, error) {
		read := reviewer.Path(ReadAccess, path, false)
		if read.Blocked() {
			return resources.DependencyPermission{HardReason: read.Reason}, nil
		}
		write, err := BuildPathPlan(cfg, session, WriteAccess, path)
		if err != nil {
			return resources.DependencyPermission{}, err
		}
		return resources.DependencyPermission{Writable: write.Allowed(), Readonly: write.Blocked()}, nil
	})
	deviceInspector := resources.NewDeviceInspector(session.AgentHasRuntimeSandbox, environment)
	resourcePath := func(mode AccessMode, raw string, op types.Op, target types.Target, complete bool) {
		pathPlan := reviewer.Path(mode, raw, containsUnresolvedPlaceholder(raw))
		if pathPlan.Blocked() || hardViolationSeen {
			add(pathPlan)
			return
		}
		result := deviceInspector.NullOffset(op, target, complete)
		if mode == WriteAccess && !(result.State == types.Known && result.NullDevice) {
			history.mayMutate = true
		}
		for _, requirement := range reviewer.ResourceRequirements(pathPlan, result, raw) {
			add(requirement)
		}
	}
	opaquePlan := func(x types.BashExecution) shellplan.BashPlan {
		p := reviewer.Opaque(x)
		versionInput := x
		if versionInput.Program == "" {
			versionInput.Program = x.CommandIdentity.Path
		}
		version := resources.ExecutionFingerprint(session.AgentHasRuntimeSandbox, resources.ExecutionVersion{Fingerprint: p.Fingerprint, RuleKey: p.RuleKey, AccessLevel: p.AccessLevel}, versionInput, variables, environment)
		p.Fingerprint, p.RuleKey = version.Fingerprint, version.RuleKey
		p.Scope, p.ScopeKind, p.ContentSHA256 = version.Scope, version.ScopeKind, version.ContentSHA256
		if x.DependencyReason != "" {
			p.Reason += "; " + x.DependencyReason
		}
		return p
	}
	if environment != nil && environment.Launch != nil && !environment.Launch.SyntaxSupported &&
		(session.AgentHasRuntimeSandbox || DirectConnectorInvocation(session, command, workingDir, variables) == nil) {
		pathReview(ReadAccess, workingDir)
		add(reviewer.WorkspaceExecution(workingDir))
		add(reviewer.UnknownDialect(*environment.Launch))
		plan := shellplan.Combine(command, accessLevel, plans)
		plan.UnknownDialect = true
		return plan
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
	globs := loopGlobs(session, workingDir)
	parsed := bashsyntax.ParseForSecurityWithKnownVariables(command, variables, globs)
	if len(session.ConnectorCLIEntries) > 0 {
		parsed = bashsyntax.ParseForExecution(command, variables, globs)
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
		mentionsExec := slices.ContainsFunc(cmd.Argv, func(word string) bool { return strings.Contains(word, "-exec") })
		for _, candidateCwd := range append([]string(nil), possibleCwds...) {
			if mentionsExec {
				history.findExecs++
				history.findExecPending++
			}
			x := analyzeBashExecution(session, cmd, candidateCwd, variables, environment)
			if depth == 0 && len(parsed.Commands) == 1 {
				call = connectorCall(command, variables, x)
			}
			if x.BlockReason != "" {
				add(reviewer.Block(x.BlockReason, "bash-access:temp-escape", cmd.Text))
			}
			for _, dep := range x.StartupDeps {
				if dep.Kind == types.EnvironmentOutput {
					pathReview(WriteAccess, dep.Path)
				}
			}
			if len(x.StartupDeps) > 0 && !hardViolationSeen {
				startup := dependencyInspector.Review(x.StartupDeps, x, history.mayMutate)
				x.DependencySHA256, x.DependencyReason = startup.SHA256, startup.Reason
				if startup.State == types.HardViolation {
					add(reviewer.Block(startup.Reason, "bash-access:dependency", x.Cwd))
				} else {
					add(reviewer.Complex(startup.Reason))
					add(opaquePlan(x))
				}
				history.mayMutate = true
			}
			allConnector = allConnector && x.Connector
			if x.ConnectorID != "" {
				connectorIDs[x.ConnectorID] = true
			}
			// executes marks code whose file effects are not described by the analysis.
			executes := x.Connector || len(x.StartupDeps) > 0
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
			if hardViolationSeen {
				continue
			}
			switch {
			case x.Connector, len(x.Argv) == 0:
			case x.Uncertain:
				executes = true
				add(reviewer.Complex("bash execution wrapper or target cannot be resolved statically"))
			case x.Opaque:
				if script, ok := unwrap.ShellScript(x.Argv); ok && x.TrustedInterpreter && len(x.StartupDeps) == 0 && depth < maxShellScriptDepth {
					// A literal `bash -c` script is analyzed like the outer command, so
					// wrappers cannot hide remote mutations or file effects.
					inner := reviewBashScript(cfg, session, script, x.Cwd, x.Env, environment, execCtx, depth+1, history)
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
				case x.Inline:
					// Literal program text is as visible as a script this Run wrote.
					add(reviewer.InlineCode(x.Identity))
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
				args, words := x.Argv[1:], x.Words[1:]
				if mentionsExec && identity.CommandFamily(x.Argv[0]) == "find" {
					if rest, restWords, inner, ok := reviewFindExec(cfg, session, x, environment, execCtx, depth); ok {
						args, words = rest, restWords
						for _, p := range inner {
							add(p)
						}
						history.findExecPending--
					}
				}
				effects := commands.Analyze(types.CommandInput{Name: identity.CommandFamily(x.Argv[0]), Args: args, Words: words, Identity: x.CommandIdentity, Dialect: dialect.Bash})
				repoDir := x.Cwd
				gitCheck := types.GitCheckNone
				executesBehavior := effects.Has(types.Exec)
				priorMutation := history.mayMutate
				dependency := dependencyInspector.Review(effects.Deps, x, priorMutation)
				x.DependencySHA256, x.DependencyReason = dependency.SHA256, dependency.Reason
				executesBehavior = executesBehavior || dependency.Unsafe
				executes = executes || dependency.HasConfig
				if dependency.State == types.Unresolved {
					add(reviewer.Complex(dependency.Reason))
				}
				if dependency.State == types.HardViolation {
					add(reviewer.Block(dependency.Reason, "bash-access:dependency", x.Cwd))
				}
				for _, effect := range effects.Ops {
					if effect.Kind == types.Exec || effect.Kind == types.RepoWrite {
						history.mayMutate = true
					}
				}
				history.mayMutate = history.mayMutate || len(effects.Unknowns) > 0
				for _, dep := range effects.Deps {
					switch dep.Kind {
					case types.GitConfig:
						gitCheck = types.GitCheck(dep.Check)
						if dep.Path != "" {
							repoDir = ResolveAgainstCwd(dep.Path, x.Cwd)
						}
					}
				}
				gitClean := false
				if gitCheck != types.GitCheckNone {
					gitState := dependencyInspector.ReviewGitExecution(x, repoDir, gitCheck, priorMutation)
					if gitState.State == types.HardViolation {
						add(reviewer.Block(gitState.Reason, "bash-access:dependency", repoDir))
					}
					if gitState.SHA256 != "" {
						x.DependencySHA256 += ":" + gitState.SHA256
					}
					gitClean = gitState.State == types.Known && !gitState.Unsafe && !executesBehavior
				}
				complete := dependency.State == types.Known && len(x.StartupDeps) == 0 && len(effects.Unknowns) == 0 && !executesBehavior && (gitCheck == types.GitCheckNone || gitClean)
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
									add(reviewer.Walk(mode, p, walkSkip(session, target)))
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
				history.mayMutate = true
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
