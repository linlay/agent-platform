package accesspolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"agent-platform/internal/bashast"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

type BashPlan struct {
	Decision    Decision
	Reason      string
	RuleKey     string
	Fingerprint string
	CommandText string
	AccessLevel string
}

type redirectAccessKind int

const (
	redirectAccessNeutral redirectAccessKind = iota
	redirectAccessRead
	redirectAccessWrite
	redirectAccessUnknown
)

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

func ReviewBashCommand(cfg config.AccessPolicyConfig, session QuerySession, command string, cwd string, variables map[string]string) BashPlan {
	accessLevel := sessionAccessLevel(session)
	level := EffectiveLevel(cfg, accessLevel)
	command = strings.TrimSpace(command)
	if command == "" {
		return bashPlan(command, accessLevel, DecisionAllow, "", "", "")
	}
	workingDir := strings.TrimSpace(cwd)
	if workingDir == "" {
		workingDir = WorkingDirectory(cfg, session)
	}
	if !filepath.IsAbs(workingDir) {
		workingDir = filepath.Join(WorkingDirectory(cfg, session), workingDir)
	}
	workingDir, _ = NormalizePath(workingDir)
	autoPlan := BashPlan{}
	if workingDir != "" {
		cwdPlan, err := BuildPathPlan(cfg, session, ReadAccess, workingDir)
		if err == nil {
			if !cwdPlan.Allowed() {
				return bashPlanFromPath(command, cwdPlan, "bash cwd is outside allowed roots")
			}
			if cwdPlan.AutoApproved() {
				autoPlan = bashPlanFromPath(command, cwdPlan, "bash cwd is outside allowed roots")
			}
		}
	}

	result := bashast.ParseForSecurityWithKnownVariables(command, variables)
	if result.Kind != bashast.Simple {
		return bashPlanForAction(command, accessLevel, level.Approvals.BashComplexFilesystem, "bash command is too complex for access-policy path analysis", "bash-access:complex")
	}
	for _, cmd := range result.Commands {
		if len(cmd.Argv) == 0 {
			continue
		}
		base := normalizedCommandBase(cmd.Argv[0])
		if isOpaqueCommand(base) {
			return opaqueBashPlan(command, accessLevel, level.Approvals.BashOpaqueCommand, base, workingDir)
		}
		for _, redirect := range cmd.Redirects {
			kind := classifyRedirectAccess(redirect)
			if kind == redirectAccessNeutral {
				continue
			}
			if strings.TrimSpace(redirect.Target) == "" || containsUnresolvedPlaceholder(redirect.Target) {
				return bashPlanForAction(command, accessLevel, level.Approvals.BashComplexFilesystem, "bash redirection target cannot be resolved statically", "bash-access:complex")
			}
			mode := ReadAccess
			switch kind {
			case redirectAccessRead:
				mode = ReadAccess
			case redirectAccessWrite:
				mode = WriteAccess
			default:
				return bashPlanForAction(command, accessLevel, level.Approvals.BashComplexFilesystem, "bash redirection target cannot be resolved statically", "bash-access:complex")
			}
			pathPlan, err := BuildPathPlan(cfg, session, mode, resolveAgainstCwd(redirect.Target, workingDir))
			if err == nil {
				review := reviewBashPathPlan(command, accessLevel, level, mode, pathPlan, "bash redirection path is outside allowed roots")
				if review.Decision == DecisionRequiresApproval || review.Decision == DecisionBlock {
					return review
				}
				if review.Decision == DecisionAutoApproved {
					autoPlan = review
				}
			}
		}
		mode := commandPathMode(base)
		for _, arg := range cmd.Argv[1:] {
			if !isPathArg(arg) {
				continue
			}
			if containsUnresolvedPlaceholder(arg) {
				return bashPlanForAction(command, accessLevel, level.Approvals.BashComplexFilesystem, "bash path argument cannot be resolved statically", "bash-access:complex")
			}
			pathPlan, err := BuildPathPlan(cfg, session, mode, resolveAgainstCwd(arg, workingDir))
			if err == nil {
				review := reviewBashPathPlan(command, accessLevel, level, mode, pathPlan, "bash path argument is outside allowed roots")
				if review.Decision == DecisionRequiresApproval || review.Decision == DecisionBlock {
					return review
				}
				if review.Decision == DecisionAutoApproved {
					autoPlan = review
				}
			}
		}
	}
	if autoPlan.Decision == DecisionAutoApproved {
		return autoPlan
	}
	return bashPlan(command, accessLevel, DecisionAllow, "", "", "")
}

func classifyRedirectAccess(redirect bashast.Redirect) redirectAccessKind {
	if redirect.IsHeredoc {
		return redirectAccessNeutral
	}
	op := strings.TrimSpace(redirect.Op)
	target := strings.TrimSpace(redirect.Target)
	if op == "" {
		return redirectAccessUnknown
	}
	if op == "<<<" {
		return redirectAccessNeutral
	}
	if isDevNullRedirectTarget(target) {
		return redirectAccessNeutral
	}
	switch op {
	case "<&", ">&":
		if isFileDescriptorRedirectTarget(target) {
			return redirectAccessNeutral
		}
		return redirectAccessUnknown
	case "<":
		return redirectAccessRead
	case ">", ">>", ">|", ">>|", "&>", "&>|", "&>>", "&>>|":
		return redirectAccessWrite
	case "<>":
		return redirectAccessWrite
	default:
		if strings.Contains(op, ">") {
			return redirectAccessWrite
		}
		if strings.Contains(op, "<") {
			return redirectAccessRead
		}
		return redirectAccessUnknown
	}
}

func isDevNullRedirectTarget(target string) bool {
	return filepath.Clean(expandHome(strings.TrimSpace(target))) == "/dev/null"
}

func isFileDescriptorRedirectTarget(target string) bool {
	target = strings.TrimSpace(target)
	if target == "-" {
		return true
	}
	if target == "" {
		return false
	}
	for _, r := range target {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func reviewBashPathPlan(command string, accessLevel string, level Level, mode AccessMode, pathPlan PathPlan, outsideReason string) BashPlan {
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

func RegisterExactApproval(execCtx *ExecutionContext, fingerprint string) {
	if execCtx == nil || strings.TrimSpace(fingerprint) == "" {
		return
	}
	if execCtx.AccessPolicyApprovals == nil {
		execCtx.AccessPolicyApprovals = map[string]int{}
	}
	execCtx.AccessPolicyApprovals[fingerprint]++
}

func RegisterRuleApproval(execCtx *ExecutionContext, ruleKey string) {
	if execCtx == nil || strings.TrimSpace(ruleKey) == "" {
		return
	}
	if execCtx.AccessPolicyRuleApprovals == nil {
		execCtx.AccessPolicyRuleApprovals = map[string]bool{}
	}
	execCtx.AccessPolicyRuleApprovals[ruleKey] = true
}

func ConsumeApproval(execCtx *ExecutionContext, plan BashPlan) bool {
	if execCtx == nil {
		return false
	}
	if execCtx.AccessPolicyRuleApprovals != nil && execCtx.AccessPolicyRuleApprovals[plan.RuleKey] {
		return true
	}
	if len(execCtx.AccessPolicyApprovals) == 0 || strings.TrimSpace(plan.Fingerprint) == "" {
		return false
	}
	remaining := execCtx.AccessPolicyApprovals[plan.Fingerprint]
	if remaining <= 0 {
		return false
	}
	if remaining == 1 {
		delete(execCtx.AccessPolicyApprovals, plan.Fingerprint)
		return true
	}
	execCtx.AccessPolicyApprovals[plan.Fingerprint] = remaining - 1
	return true
}

func HasApproval(execCtx *ExecutionContext, plan BashPlan) bool {
	if execCtx == nil {
		return false
	}
	if execCtx.AccessPolicyRuleApprovals != nil && execCtx.AccessPolicyRuleApprovals[plan.RuleKey] {
		return true
	}
	return execCtx.AccessPolicyApprovals != nil && execCtx.AccessPolicyApprovals[plan.Fingerprint] > 0
}

func bashPlanFromPath(command string, pathPlan PathPlan, reason string) BashPlan {
	decision := pathPlan.Decision
	ruleKey := "bash-access:path:" + pathPlan.RuleKey
	fingerprintInput := command + "\x00" + pathPlan.Fingerprint
	sum := sha256.Sum256([]byte(fingerprintInput))
	return BashPlan{
		Decision:    decision,
		Reason:      firstNonBlank(reason, pathPlan.Reason),
		RuleKey:     ruleKey,
		Fingerprint: hex.EncodeToString(sum[:]),
		CommandText: command,
		AccessLevel: pathPlan.AccessLevel,
	}
}

func bashWriteInWriteRootsPlan(command string, accessLevel string, action string, pathPlan PathPlan) BashPlan {
	decision := decisionForAction(action)
	if decision == DecisionAllow {
		return BashPlan{Decision: DecisionAllow}
	}
	ruleKey := "bash-access:write-root:" + pathPlan.RuleKey
	fingerprintInput := command + "\x00" + pathPlan.Fingerprint + "\x00write-root"
	return bashPlan(command, accessLevel, decision, "bash write path is under an allowed write root", ruleKey, fingerprintInput)
}

func bashPlanForAction(command string, accessLevel string, action string, reason string, ruleKey string) BashPlan {
	return bashPlan(command, accessLevel, decisionForAction(action), reason, ruleKey, command)
}

func opaqueBashPlan(command string, accessLevel string, action string, base string, cwd string) BashPlan {
	hash := sha256.Sum256([]byte(base + "\x00" + cwd))
	return bashPlan(command, accessLevel, decisionForAction(action), fmt.Sprintf("bash command %q may access files internally", base), "bash-access:opaque:"+hex.EncodeToString(hash[:8]), command)
}

func bashPlan(command string, accessLevel string, decision Decision, reason string, ruleKey string, fingerprintInput string) BashPlan {
	if strings.TrimSpace(ruleKey) == "" {
		ruleKey = "bash-access"
	}
	if strings.TrimSpace(fingerprintInput) == "" {
		fingerprintInput = command + "\x00" + reason + "\x00" + ruleKey
	}
	sum := sha256.Sum256([]byte(fingerprintInput))
	return BashPlan{
		Decision:    decision,
		Reason:      strings.TrimSpace(reason),
		RuleKey:     ruleKey,
		Fingerprint: hex.EncodeToString(sum[:]),
		CommandText: command,
		AccessLevel: accessLevel,
	}
}

func normalizedCommandBase(command string) string {
	return strings.ToLower(filepath.Base(strings.TrimSpace(command)))
}

func isOpaqueCommand(base string) bool {
	switch base {
	case "go", "npm", "npx", "yarn", "pnpm", "node", "python", "python3", "pip", "make", "bash", "sh":
		return true
	default:
		return false
	}
}

func commandPathMode(base string) AccessMode {
	switch base {
	case "mkdir", "touch", "cp", "mv", "rm", "ln", "chmod", "tee":
		return WriteAccess
	default:
		return ReadAccess
	}
}

func isPathArg(arg string) bool {
	arg = strings.TrimSpace(arg)
	if arg == "" || strings.Contains(arg, "://") || strings.HasPrefix(arg, "git@") {
		return false
	}
	if strings.HasPrefix(arg, "-") {
		if _, value, ok := strings.Cut(arg, "="); ok {
			arg = strings.TrimSpace(value)
		} else {
			return false
		}
	}
	return strings.HasPrefix(arg, "~") ||
		filepath.IsAbs(arg) ||
		strings.HasPrefix(arg, "../") ||
		arg == ".." ||
		strings.HasPrefix(arg, "./") ||
		strings.Contains(arg, string(filepath.Separator))
}

func resolveAgainstCwd(raw string, cwd string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || filepath.IsAbs(raw) || strings.HasPrefix(raw, "~") {
		return raw
	}
	if strings.TrimSpace(cwd) == "" {
		return raw
	}
	return filepath.Join(cwd, raw)
}

func containsUnresolvedPlaceholder(value string) bool {
	return strings.Contains(value, bashast.CommandSubstitutionPlaceholder) ||
		strings.Contains(value, bashast.TrackedVariablePlaceholder)
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
