// Package compaction contains transport-independent context budget policy.
package compaction

const (
	ToolsPercent    = 90
	SummaryPercent  = 90
	TargetPercent   = 60
	KeepRecentTools = 5
)

// Reached rounds the threshold upwards, so 89.99% never triggers 90%.
func Reached(tokens, window, percent int) bool {
	return window > 0 && tokens >= (window*percent+99)/100
}

func Target(window int) int { return window * TargetPercent / 100 }

// SummaryBudget returns the summary input budget and the summary length
// target. The target is a prompt instruction and the post-compact planning
// reserve, not a provider output cap: the summary request leaves output open
// and reserves the target only so the requested length fits beside the input.
func SummaryBudget(window, retainedTokens int) (input, target int) {
	target = min(4096, window/10, window-retainedTokens-64)
	if target <= 0 {
		return 0, 0
	}
	return max(0, window-target-64), target
}

// KeepRecentRounds is the L1 completed-model-call protection window.
func KeepRecentRounds(window, override int) int {
	if override >= 5 && override <= 10 {
		return override
	}
	if window >= 1000000 {
		return 10
	}
	if window > 200000 {
		return 7
	}
	return 5
}
