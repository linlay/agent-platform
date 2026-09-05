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

func mergeRunUsageData(target *chat.UsageData, incoming chat.UsageData) {
	runexec.MergeRunUsageData(target, incoming)
}

func addEstimatedUsageCost(target *chat.UsageData, delta chat.UsageData) {
	runexec.AddEstimatedUsageCost(target, delta)
}

func estimatedCostFromMap(usage map[string]any) map[string]any {
	return runexec.EstimatedCostFromMap(usage)
}

func floatValue(value any) float64 {
	return runexec.FloatValue(value)
}

func usageDetailInt(usage map[string]any, detailKey string, valueKey string) int {
	return runexec.UsageDetailInt(usage, detailKey, valueKey)
}

func applyUsageTimingFromMap(target *chat.UsageData, usage map[string]any) {
	runexec.ApplyUsageTimingFromMap(target, usage)
}

func addUsageData(base chat.UsageData, delta chat.UsageData) chat.UsageData {
	return runexec.AddUsageData(base, delta)
}

func addUsageTimingMap(out map[string]any, usage chat.UsageData) {
	runexec.AddUsageTimingMap(out, usage)
}

func usageDataMap(usage chat.UsageData) map[string]any {
	return runexec.UsageDataMap(usage)
}

func usageDataMapForSnapshot(usage chat.UsageData) map[string]any {
	return runexec.UsageDataMapForSnapshot(usage)
}

func usageDataMapWithOptions(usage chat.UsageData, includeZeroToolCallCount bool) map[string]any {
	return runexec.UsageDataMapWithOptions(usage, includeZeroToolCallCount)
}

func mergedUsageModelKey(base chat.UsageData, delta chat.UsageData) string {
	return runexec.MergedUsageModelKey(base, delta)
}

func usageHasData(usage chat.UsageData) bool {
	return runexec.UsageHasData(usage)
}
