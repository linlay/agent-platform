// Package plan constructs Shell access decisions and binds and consumes their approvals.
package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	. "agent-platform/internal/accesspolicy"
	"agent-platform/internal/shellreview/types"
)

// BashPlan carries an access decision and the leaf requirements that approval must cover.
type BashPlan struct {
	// ReviewCommand excludes verified connector payloads, for analysis only.
	ReviewCommand string
	HasConnector  bool
	ConnectorOnly bool
	// ConnectorIDs lists the mounted connectors the command executes.
	ConnectorIDs []string
	// ConnectorCall is set when the whole command is one plain connector call.
	ConnectorCall *types.ConnectorCall
	// Requirements are leaf decisions; a rule approval never approves sibling requirements.
	Syntax        bool // the selected requirement comes from shell syntax safety
	SyntaxLevel   int
	Requirements  []BashPlan
	Decision      Decision
	Reason        string
	RuleKey       string
	Fingerprint   string
	CommandText   string
	AccessLevel   string
	Scope         string
	ScopeKind     string
	ContentSHA256 string
	// UnknownDialect prevents the Bash-only safety parser from approving or
	// rejecting text executed by another grammar. Access policy reviews it whole.
	UnknownDialect    bool
	LaunchFingerprint string
	Launch            *types.LaunchBinding
	// UsesSSHAgent marks Git network operations that may authenticate with the
	// user's SSH agent; only then is SSH_AUTH_SOCK passed to the process.
	UsesSSHAgent bool
	// FindExecReviewed marks a command whose every find -exec was shown
	// read-only, so the syntax review drops its find -exec requirement.
	FindExecReviewed bool
}

func (p BashPlan) Allowed() bool {
	return p.Decision == DecisionAllow || p.Decision == DecisionAutoApproved
}

func (p BashPlan) RequiresApproval() bool {
	return p.Decision == DecisionRequiresApproval
}

func (p BashPlan) AutoApproved() bool {
	return p.Decision == DecisionAutoApproved
}

func (p BashPlan) Blocked() bool {
	return p.Decision == DecisionBlock
}

func (p BashPlan) ShellReviewCommand(command string) string {
	if p.HasConnector {
		return p.ReviewCommand
	}
	return command
}

// ReviewPath preserves a path decision and applies the configured Bash write-root action.
func ReviewPath(command string, accessLevel string, level Level, mode AccessMode, pathPlan PathPlan, outsideReason string) BashPlan {
	if pathPlan.AutoApproved() {
		return bashPlanFromPath(command, pathPlan, outsideReason)
	}
	if !pathPlan.Allowed() {
		return bashPlanFromPath(command, pathPlan, outsideReason)
	}
	if mode == WriteAccess {
		return bashWriteInWriteRootsPlan(command, accessLevel, level.Approvals.BashWriteInWriteRoots, pathPlan)
	}
	return BashPlan{Decision: DecisionAllow}
}

func bashPlanFromPath(command string, pathPlan PathPlan, reason string) BashPlan {
	decision := pathPlan.Decision
	ruleKey := "bash-access:path:" + pathPlan.RuleKey
	fingerprintInput := command + "\x00" + pathPlan.Fingerprint
	sum := sha256.Sum256([]byte(strings.Join([]string{"shell-plan:v2", ruleKey, pathPlan.AccessLevel, fingerprintInput}, "\x00")))
	return BashPlan{
		Decision:    decision,
		Reason:      firstNonBlank(reason, pathPlan.Reason),
		RuleKey:     ruleKey,
		Fingerprint: hex.EncodeToString(sum[:]),
		CommandText: command,
		AccessLevel: pathPlan.AccessLevel,
		Scope:       pathPlan.Path,
		ScopeKind:   "exact_target",
	}
}

func bashWriteInWriteRootsPlan(command string, accessLevel string, action string, pathPlan PathPlan) BashPlan {
	decision := DecisionForAction(action)
	if decision == DecisionAllow {
		return BashPlan{Decision: DecisionAllow}
	}
	ruleKey := "bash-access:write-root:" + pathPlan.RuleKey
	fingerprintInput := command + "\x00" + pathPlan.Fingerprint + "\x00write-root"
	return New(command, accessLevel, decision, "bash write path is under an allowed write root", ruleKey, fingerprintInput)
}

// ForAction builds a requirement from a configured approval action.
func ForAction(command string, accessLevel string, action string, reason string, ruleKey string) BashPlan {
	return New(command, accessLevel, DecisionForAction(action), reason, ruleKey, command)
}

// Opaque builds the requirement for a program whose internal file effects are unknown.
func Opaque(command string, accessLevel string, action string, base string, cwd string) BashPlan {
	hash := sha256.Sum256([]byte(base + "\x00" + cwd))
	return New(command, accessLevel, DecisionForAction(action), fmt.Sprintf("bash command %q may access files internally; approval covers this invocation, content version and cwd %q", base, cwd), "bash-access:opaque:"+hex.EncodeToString(hash[:8]), command)
}

// New builds a leaf requirement with its existing command and fingerprint contract.
func New(command string, accessLevel string, decision Decision, reason string, ruleKey string, fingerprintInput string) BashPlan {
	if strings.TrimSpace(ruleKey) == "" {
		ruleKey = "bash-access"
	}
	if strings.TrimSpace(fingerprintInput) == "" {
		fingerprintInput = command + "\x00" + reason + "\x00" + ruleKey
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{"shell-plan:v2", ruleKey, accessLevel, fingerprintInput}, "\x00")))
	return BashPlan{
		Decision:    decision,
		Reason:      strings.TrimSpace(reason),
		RuleKey:     ruleKey,
		Fingerprint: hex.EncodeToString(sum[:]),
		CommandText: command,
		AccessLevel: accessLevel,
	}
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// ForInvocation approves one exact invocation class; the run grant reuses only
// the same command text in the same working directory.
func ForInvocation(command, accessLevel, action, reason, rule, cwd string) BashPlan {
	sum := sha256.Sum256([]byte(command + "\x00" + cwd))
	return New(command, accessLevel, DecisionForAction(action), reason, rule+":"+hex.EncodeToString(sum[:8]), command+"\x00"+cwd)
}

// ErrorCode preserves the public error category while all gates share a plan.
func (p BashPlan) ErrorCode() string {
	if p.Syntax {
		if p.Blocked() {
			return "bash_security_blocked"
		}
		return "bash_security_approval_required"
	}
	if p.Blocked() {
		return "bash_access_blocked"
	}
	return "bash_access_approval_required"
}
