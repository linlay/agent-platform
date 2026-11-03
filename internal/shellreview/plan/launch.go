package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	. "agent-platform/internal/accesspolicy"
	"agent-platform/internal/shellreview/types"
)

// BindLaunch freezes exact grants to the complete invocation and reusable
// rules to shell identity/template/dialect. Original rule keys keep their own
// target or invocation constraints; cwd/environment are not added to path rules.
func BindLaunch(p BashPlan, launch types.LaunchBinding) BashPlan {
	if launch.Fingerprint == "" || launch.ContractFingerprint == "" || launch.RuleFingerprint == "" {
		p.Decision, p.Reason = DecisionBlock, "shell launch binding is incomplete"
		return p
	}
	// Bind visible dialect/profile facts as well as the backend's opaque hashes.
	// Partial or stale backend hashes must not make two different contracts share
	// an exact or run-rule capability.
	facts, _ := json.Marshal(struct {
		Executable string
		Template   []string
		Dialect    string
		Supported  bool
	}{launch.Executable, launch.Template, string(launch.Dialect), launch.SyntaxSupported})
	ruleHash := sha256.Sum256(append([]byte(launch.RuleFingerprint+"\x00"), facts...))
	ruleFingerprint := hex.EncodeToString(ruleHash[:])
	for i := range p.Requirements {
		p.Requirements[i] = BindLaunch(p.Requirements[i], launch)
	}
	if len(p.Requirements) > 0 {
		// Keep aggregation identical to PendingBashPlan: approving a combined
		// plan must approve that same plan on the next preflight/final review.
		combined := Combine(p.CommandText, p.AccessLevel, p.Requirements)
		p.Decision, p.Reason = combined.Decision, combined.Reason
		p.RuleKey, p.Fingerprint = combined.RuleKey, combined.Fingerprint
		p.Requirements = combined.Requirements
	} else {
		sum := sha256.Sum256([]byte(p.Fingerprint + "\x00" + p.RuleKey + "\x00" + p.AccessLevel + "\x00" + launch.Fingerprint + "\x00" + ruleFingerprint))
		p.Fingerprint = hex.EncodeToString(sum[:])
		if p.RequiresApproval() || p.AutoApproved() {
			p.RuleKey += ":launch:" + ruleFingerprint
		}
	}
	p.LaunchFingerprint = launch.Fingerprint
	p.Launch = &launch
	return p
}
