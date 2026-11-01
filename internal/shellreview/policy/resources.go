package policy

import (
	shellplan "agent-platform/internal/shellreview/plan"
	"agent-platform/internal/shellreview/types"
)

// ResourceRequirements never lets an offset erase a path/admin hard block.
// Uncertainty preserves the original path requirement and adds a complex leaf.
func (r Reviewer) ResourceRequirements(pathPlan shellplan.BashPlan, result types.ResourceResult, raw string) []shellplan.BashPlan {
	if pathPlan.Blocked() {
		return []shellplan.BashPlan{pathPlan}
	}
	switch result.State {
	case types.HardViolation:
		return []shellplan.BashPlan{r.Block(result.Reason, "bash-access:resource", raw)}
	case types.Known:
		if result.NullDevice {
			return nil
		}
		return []shellplan.BashPlan{pathPlan}
	default:
		reason := result.Reason
		if reason == "" {
			reason = "resource identity could not be confirmed"
		}
		return []shellplan.BashPlan{pathPlan, r.Complex(reason)}
	}
}
