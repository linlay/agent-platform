package policy

import (
	"runtime"

	. "agent-platform/internal/accesspolicy"
	"agent-platform/internal/shellreview/types"
)

// LegacyOutputExempt preserves the old curl/wget-only rule until C11u replaces
// it with verified resource facts. It never offsets a blocked path requirement.
func (r Reviewer) LegacyOutputExempt(base string, incomplete bool, file types.FileAccess) bool {
	if !incomplete && (base == "curl" || base == "wget") && file.Write && file.Path == "/dev/null" && (r.session.AgentHasRuntimeSandbox || runtime.GOOS != "windows") {
		if p, err := BuildPathPlan(r.cfg, r.session, WriteAccess, file.Path); err == nil && !p.Blocked() {
			return true
		}
	}
	return false
}
