package server

import (
	"agent-platform/internal/chat"
	"agent-platform/internal/runtime/runexec"
)

// These narrow aliases keep the existing server call sites stable while
// usage accounting lives in runtime/runexec. They disappear as the remaining
// executor and proxy recorders move behind their runtime services.
func usageDataFromMap(usage map[string]any) chat.UsageData {
	return runexec.UsageDataFromMap(usage)
}

func mergeUsageMapIntoRunData(target *chat.UsageData, usage map[string]any) {
	runexec.MergeUsageMapIntoRunData(target, usage)
}

func addEstimatedUsageCost(target *chat.UsageData, delta chat.UsageData) {
	runexec.AddEstimatedUsageCost(target, delta)
}

func floatValue(value any) float64 {
	return runexec.FloatValue(value)
}

func addUsageData(base chat.UsageData, delta chat.UsageData) chat.UsageData {
	return runexec.AddUsageData(base, delta)
}

func usageDataMap(usage chat.UsageData) map[string]any {
	return runexec.UsageDataMap(usage)
}

func usageDataMapForSnapshot(usage chat.UsageData) map[string]any {
	return runexec.UsageDataMapForSnapshot(usage)
}

func usageHasData(usage chat.UsageData) bool {
	return runexec.UsageHasData(usage)
}
