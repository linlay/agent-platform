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

// SummaryBudget reserves output and protocol overhead before opening a model
// stream. The output must also fit beside the unmodified retained context.
func SummaryBudget(window, retainedTokens int) (input, output int) {
	output = min(4096, window/10, window-retainedTokens-64)
	if output <= 0 {
		return 0, 0
	}
	return max(0, window-output-64), output
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
