package kbase

import (
	"path/filepath"
	"strings"
)

func normalizeStoragePath(storageDir string) string {
	storageDir = filepath.Clean(strings.TrimSpace(storageDir))
	if storageDir == "." || storageDir == "" {
		return storageDir
	}
	if abs, err := filepath.Abs(storageDir); err == nil {
		return abs
	}
	return storageDir
}

// canonicalStoragePath resolves the longest existing path prefix so a storage
// directory that has not been created yet still shares identity across
// symlinked parents.
func canonicalStoragePath(storageDir string) string {
	normalized := normalizeStoragePath(storageDir)
	if normalized == "" || normalized == "." {
		return normalized
	}
	for candidate := normalized; ; candidate = filepath.Dir(candidate) {
		if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
			remainder, relErr := filepath.Rel(candidate, normalized)
			if relErr == nil {
				if remainder == "." {
					return normalizeStoragePath(resolved)
				}
				return normalizeStoragePath(filepath.Join(resolved, remainder))
			}
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			break
		}
	}
	return normalized
}

func storageLockKey(storageDir string) string {
	return canonicalStoragePath(storageDir)
}
