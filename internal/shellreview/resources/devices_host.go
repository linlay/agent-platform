package resources

import (
	"agent-platform/internal/shellreview/types"
	"os"
)

func inspectHostNullDevice() types.ResourceResult { return inspectHostNullAt("/dev/null") }
func inspectHostNullAt(path string) types.ResourceResult {
	info, err := os.Lstat(path)
	if err != nil {
		return unresolvedDevice("host null device unavailable")
	}
	if info.Mode()&os.ModeCharDevice == 0 || info.Mode()&os.ModeSymlink != 0 {
		return types.ResourceResult{State: types.Known}
	}
	return hostNullStat(info)
}
