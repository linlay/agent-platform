package catalog

import (
	"path/filepath"
	"strings"
)

const (
	exampleSuffix = ".example"
	demoSuffix    = ".demo"
)

func ShouldLoadRuntimeName(rawName string) bool {
	return strings.TrimSpace(rawName) != "" && !isMarkedRuntimeName(rawName, exampleSuffix)
}

func LogicalRuntimeBaseName(rawName string) string {
	name := strings.TrimSpace(rawName)
	if name == "" {
		return ""
	}
	ext := filepath.Ext(name)
	stem := name
	if ext != "" {
		stem = strings.TrimSuffix(name, ext)
	}
	lowerStem := strings.ToLower(stem)
	switch {
	case strings.HasSuffix(lowerStem, exampleSuffix):
		return stem[:len(stem)-len(exampleSuffix)]
	case strings.HasSuffix(lowerStem, demoSuffix):
		return stem[:len(stem)-len(demoSuffix)]
	default:
		return stem
	}
}

func isMarkedRuntimeName(rawName string, marker string) bool {
	name := strings.ToLower(strings.TrimSpace(rawName))
	if name == "" {
		return false
	}
	if strings.HasSuffix(name, marker) {
		return true
	}
	ext := filepath.Ext(name)
	if ext == "" {
		return false
	}
	return strings.HasSuffix(strings.TrimSuffix(name, ext), marker)
}
