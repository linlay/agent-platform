// Package policy maps reviewed Shell facts to the existing access requirements.
package policy

import (
	"fmt"

	. "agent-platform/internal/accesspolicy"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	shellplan "agent-platform/internal/shellreview/plan"
	"agent-platform/internal/shellreview/types"
)

// Reviewer holds the existing policy inputs for one command review.
type Reviewer struct {
	cfg         config.AccessPolicyConfig
	session     QuerySession
	command     string
	level       Level
	accessLevel string
}

func New(cfg config.AccessPolicyConfig, session QuerySession, command string) Reviewer {
	accessLevel := SessionAccessLevel(session)
	return Reviewer{cfg: cfg, session: session, command: command, level: EffectiveLevel(cfg, accessLevel), accessLevel: accessLevel}
}

func (r Reviewer) Allow() shellplan.BashPlan { return shellplan.BashPlan{Decision: DecisionAllow} }

func KeepRequirement(p shellplan.BashPlan) bool {
	return p.Decision != DecisionAllow || p.RuleKey == "bash-access:authored-script" || p.RuleKey == "bash-access:skill-script"
}

func (r Reviewer) Complex(reason string) shellplan.BashPlan {
	return shellplan.ForAction(r.command, r.accessLevel, r.level.Approvals.BashComplexFilesystem, reason, "bash-access:complex")
}

func (r Reviewer) Path(mode AccessMode, raw string, unresolved bool) shellplan.BashPlan {
	if unresolved {
		return r.Complex("bash path cannot be resolved statically")
	}
	p, err := BuildPathPlan(r.cfg, r.session, mode, raw)
	if err != nil {
		return r.Block(err.Error(), "bash-access:path-invalid", raw)
	}
	reason := "bash path is outside allowed roots: " + p.Path
	switch {
	case p.Blocked():
		reason = "bash path blocked: " + p.Reason + ": " + p.Path
	case p.RequiresApproval() && p.Reason != OutsideRootsReason(mode):
		reason = "bash path requires approval: " + p.Reason + ": " + p.Path
	}
	return shellplan.ReviewPath(r.command, r.accessLevel, r.level, mode, p, reason)
}

func (r Reviewer) UnknownDialect(launch types.LaunchBinding) shellplan.BashPlan {
	// Unsupported grammar always needs an exact human decision in restricted levels.
	decision := DecisionForAction(r.level.Approvals.BashComplexFilesystem)
	if decision != DecisionBlock && r.accessLevel != AccessLevelFullAccess {
		decision = DecisionRequiresApproval
	}
	return shellplan.New(r.command, r.accessLevel, decision,
		fmt.Sprintf("shell syntax is not analyzed (%s); executable=%q; startup=%q: %s", launch.Dialect, launch.Executable, launch.Template, launch.Reason),
		"bash-access:complex:dialect", r.command)
}

func (r Reviewer) Opaque(x types.BashExecution) shellplan.BashPlan {
	return shellplan.Opaque(r.command, r.accessLevel, r.level.Approvals.BashOpaqueCommand, x.Identity, x.Cwd)
}

func (r Reviewer) OpaqueBlocked() bool {
	return DecisionForAction(r.level.Approvals.BashOpaqueCommand) == DecisionBlock
}

func (r Reviewer) SkillScript(path string) shellplan.BashPlan {
	return shellplan.New(r.command, r.accessLevel, DecisionAllow, "script matches this run's selected skill", "bash-access:skill-script", path)
}

func (r Reviewer) AuthoredScript(path string) shellplan.BashPlan {
	return shellplan.New(r.command, r.accessLevel, DecisionAllow, "script was written by this run in the current Chat directory", "bash-access:authored-script", path)
}

func (r Reviewer) Destructive(cwd string) shellplan.BashPlan {
	return shellplan.ForInvocation(r.command, r.accessLevel, r.level.Approvals.Destructive, "command discards data (recursive deletion or uncommitted changes)", "bash-access:destructive", cwd)
}

func (r Reviewer) RemoteMutation() shellplan.BashPlan {
	return shellplan.ForAction(r.command, r.accessLevel, r.level.Approvals.RemoteMutation, "command modifies a remote resource", "bash-access:remote")
}

func (r Reviewer) Connector(p shellplan.BashPlan) shellplan.BashPlan {
	if p.Decision == DecisionAllow {
		p.RuleKey = "bash-access:connector"
		p.Reason = "mounted connector CLI"
	}
	return p
}
