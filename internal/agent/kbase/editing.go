package kbase

import "strings"

var editingToolNames = []string{
	ToolSearch,
	ToolFiles,
	ToolRead,
	ToolStatus,
	ToolRefresh,
	ToolDatetime,
	"file_read",
	"file_glob",
	"file_grep",
	"file_write",
	"file_edit",
}

func IsMode(mode string) bool {
	return strings.EqualFold(strings.TrimSpace(mode), Mode)
}

func EditingModeEnabled(mode string, requested bool) bool {
	return requested && IsMode(mode)
}

func EditingToolNames() []string {
	return append([]string(nil), editingToolNames...)
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
