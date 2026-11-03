package resources

import (
	"agent-platform/internal/shellreview/types"
	"os"
	"syscall"
)

func hostNullStat(info os.FileInfo) types.ResourceResult {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return unresolvedDevice("host device identity unavailable")
	}
	dev := uint32(stat.Rdev)
	return types.ResourceResult{State: types.Known, NullDevice: dev>>24 == 3 && dev&0xffffff == 2}
}
