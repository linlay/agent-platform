package catalog

import (
	"path/filepath"
	"strings"
)

const (
	exampleSuffix                = ".example"
	demoSuffix                   = ".demo"
	connectorImportStagingPrefix = ".connector-import-"
)

// ShouldLoadRuntimeName applies to both runtime file names and directory names.
func ShouldLoadRuntimeName(rawName string) bool {
	return strings.TrimSpace(rawName) != "" && !isMarkedRuntimeName(rawName, exampleSuffix)
}

// ShouldIgnoreRuntimeWatchPath returns true for filesystem noise and
// API-managed files that must not trigger a second runtime reload.
func ShouldIgnoreRuntimeWatchPath(path string) bool {
	// Inspect every component, including Windows paths in cross-platform tests.
	for _, component := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if isRuntimeTransactionName(component) {
			return true
		}
	}
	name := filepath.Base(filepath.Clean(strings.TrimSpace(path)))
	return name == ".DS_Store" ||
		name == AgentOrderFileName ||
		(strings.HasPrefix(name, ".agent-order-") && strings.HasSuffix(name, ".json"))
}

// ShouldWatchRuntimeDir returns true if a directory should be recursively
// watched by the file watcher. Returns false for staging directories
// (e.g. "cutej.bootstrap"), backup snapshots (containing ".bak."), and
// post-init leftovers (e.g. "bootstrap.deleted"). Keeping this close to
// ShouldLoadRuntimeName ensures watch and load filtering stay consistent.
func ShouldWatchRuntimeDir(name string) bool {
	if !ShouldLoadRuntimeName(name) || isRuntimeTransactionName(name) {
		return false
	}
	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, connectorImportStagingPrefix) {
		return false
	}
	if strings.HasPrefix(lower, editableSkillImportStagingPrefix) {
		return false
	}
	if strings.HasPrefix(lower, editableAgentImportStagingPrefix) || strings.HasPrefix(lower, editableAgentImportBackupPrefix) {
		return false
	}
	// Staging directories created before bootstrap renames them.
	if strings.HasSuffix(lower, ".bootstrap") {
		return false
	}
	// Backup snapshots created by bootstrap or manual operations.
	if strings.Contains(lower, ".bak.") {
		return false
	}
	// Post-init leftovers.
	if strings.HasSuffix(lower, ".deleted") {
		return false
	}
	return true
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

// Transaction content and package metadata are never live catalog inputs.
func isRuntimeTransactionName(name string) bool {
	name = strings.ToLower(name)
	return name == skillPackageStateDirName ||
		strings.HasPrefix(name, ".skill-package-") ||
		strings.HasPrefix(name, ".skill-backup-") ||
		strings.HasPrefix(name, editableSkillImportStagingPrefix) ||
		strings.HasPrefix(name, editableAgentImportStagingPrefix) ||
		strings.HasPrefix(name, ".connector-import-") ||
		strings.HasPrefix(name, ".connector-delete-") ||
		strings.HasPrefix(name, ".connector-backup-") ||
		strings.HasPrefix(name, ".backup-") || strings.Contains(name, ".backup-")
}
