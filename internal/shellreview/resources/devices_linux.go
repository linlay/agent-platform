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
	dev := uint64(stat.Rdev)
	major := (dev>>8)&0xfff | (dev>>32)&0xfffff000
	minor := dev&0xff | (dev>>12)&0xffffff00
	return types.ResourceResult{State: types.Known, NullDevice: major == 1 && minor == 3}
}
