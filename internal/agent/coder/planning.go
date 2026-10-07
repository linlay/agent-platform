package coder

import (
	"strings"

	"agent-platform/internal/contracts"
)

func IsMode(mode string) bool {
	return strings.EqualFold(strings.TrimSpace(mode), Mode)
}

func IsACPBackend(mode string, acpBridgeID string) bool {
	return IsMode(mode) && strings.TrimSpace(acpBridgeID) != ""
}

func IsNativeBackend(mode string, acpBridgeID string) bool {
	return IsMode(mode) && strings.TrimSpace(acpBridgeID) == ""
}

func RuntimeToolNamesForAgent(mode string, acpBridgeID string, stage string, toolNames []string) []string {
	if !IsNativeBackend(mode, acpBridgeID) {
		return append([]string(nil), toolNames...)
	}
	return RuntimeToolNamesForStage(mode, stage, toolNames)
}

// RuntimeToolNamesForStage adds the plan task tools CODER always offers outside
// planning; a planning Run keeps exactly the tools planning-mode selected.
func RuntimeToolNamesForStage(mode string, stage string, toolNames []string) []string {
	out := append([]string(nil), toolNames...)
	if !IsMode(mode) {
		return out
	}
	if strings.EqualFold(strings.TrimSpace(stage), MainStage) {
		return contracts.AppendPlanTaskToolNames(out)
	}
	return out
}
