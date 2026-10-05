package tools

import (
	. "agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
)

func computeLineDiffStats(before string, after string) LineDiffStats {
	return filetools.ComputeLineDiffStats(before, after)
}

func lineStatsPayload(stats LineDiffStats) map[string]any {
	return filetools.LineStatsPayload(stats)
}
