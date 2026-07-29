package kbase

import "strings"

func IsMode(mode string) bool {
	return strings.EqualFold(strings.TrimSpace(mode), Mode)
}

func EditingModeEnabled(mode string, requested bool) bool {
	return requested && IsMode(mode)
}

func EditingToolNames() []string {
	return DefaultToolNames()
}

func RuntimeStage(editingMode bool) string {
	if editingMode {
		return EditingStage
	}
	return MainStage
}

func SystemInitCacheKey(stage string) string {
	if strings.EqualFold(strings.TrimSpace(stage), EditingStage) {
		return EditingCacheKey
	}
	return MainCacheKey
}
