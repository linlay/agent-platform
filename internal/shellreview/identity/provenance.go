package identity

import (
	. "agent-platform/internal/accesspolicy"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/shellreview/types"
)

// AuthoredScriptExempt accepts a script that this Run wrote through the managed
// file tools, located in the current Chat directory and started by a verified
// system interpreter (directly or through its shebang).
func AuthoredScriptExempt(session QuerySession, ctx *ExecutionContext, x types.BashExecution, env *types.BashEnvironment) bool {
	if ctx == nil || ctx.AuthoredScripts == nil || x.Script == "" || x.Wrapped {
		return false
	}
	if !x.TrustedInterpreter && !IsInterpreter(x.Identity) {
		return false
	}
	target := ResolveAgainstCwd(x.Script, x.Cwd)
	host, ok := ExecutableHostPath(session, target)
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

func SkillExecutionMatches(session QuerySession, ctx *ExecutionContext, target string, env *types.BashEnvironment) bool {
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
