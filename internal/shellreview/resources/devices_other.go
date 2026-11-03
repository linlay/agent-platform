//go:build !linux && !darwin

package resources

import (
	"agent-platform/internal/shellreview/types"
	"os"
)

func hostNullStat(os.FileInfo) types.ResourceResult {
	return unresolvedDevice("host device identity unsupported on this OS")
}
