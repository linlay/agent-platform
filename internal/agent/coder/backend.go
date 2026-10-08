package coder

import "strings"

func IsMode(mode string) bool {
	return strings.EqualFold(strings.TrimSpace(mode), Mode)
}

func IsACPBackend(mode string, acpBridgeID string) bool {
	return IsMode(mode) && strings.TrimSpace(acpBridgeID) != ""
}

func IsNativeBackend(mode string, acpBridgeID string) bool {
	return IsMode(mode) && strings.TrimSpace(acpBridgeID) == ""
}
