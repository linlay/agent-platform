package tools

import . "agent-platform/internal/contracts"

func computeLineDiffStats(before string, after string) LineDiffStats {
	return ComputeLineDiffStats(before, after)
}

func lineStatsPayload(stats LineDiffStats) map[string]any {
	return LineStatsPayload(stats)
}
