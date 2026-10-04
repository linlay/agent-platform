package server

import (
	"agent-platform/internal/chat"
	"agent-platform/internal/runtime/runexec"
)

// Remaining response collectors and public usage mappers share runtime helpers.
func mergeUsageMapIntoRunData(target *chat.UsageData, usage map[string]any) {
	runexec.MergeUsageMapIntoRunData(target, usage)
}

func floatValue(value any) float64 {
	return runexec.FloatValue(value)
}
