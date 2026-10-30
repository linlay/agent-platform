package accesspolicy

import (
	"crypto/sha256"
	"encoding/hex"

	"agent-platform/internal/shellreview/types"
)

// bindBashLaunch freezes exact grants to the complete invocation and reusable
// rules to shell identity/template/dialect. Original rule keys keep their own
// target or invocation constraints; cwd/environment are not added to path rules.
func bindBashLaunch(p BashPlan, launch types.LaunchBinding) BashPlan {
	if launch.Fingerprint == "" || launch.ContractFingerprint == "" || launch.RuleFingerprint == "" {
		p.Decision, p.Reason = DecisionBlock, "shell launch binding is incomplete"
		return p
	}
	for i := range p.Requirements {
		p.Requirements[i] = bindBashLaunch(p.Requirements[i], launch)
	}
	if len(p.Requirements) > 0 {
		// Keep aggregation identical to PendingBashPlan: approving a combined
		// plan must approve that same plan on the next preflight/final review.
		combined := combineBashPlans(p.CommandText, p.AccessLevel, p.Requirements)
		p.Decision, p.Reason = combined.Decision, combined.Reason
		p.RuleKey, p.Fingerprint = combined.RuleKey, combined.Fingerprint
		p.Requirements = combined.Requirements
	} else {
		sum := sha256.Sum256([]byte(p.Fingerprint + "\x00" + launch.Fingerprint))
		p.Fingerprint = hex.EncodeToString(sum[:])
		if p.RequiresApproval() || p.AutoApproved() {
			p.RuleKey += ":launch:" + launch.RuleFingerprint
		}
	}
	p.LaunchFingerprint = launch.Fingerprint
	p.Launch = &launch
	return p
}
