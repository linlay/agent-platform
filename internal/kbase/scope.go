package kbase

import (
	"path/filepath"
	"strings"
)

func normalizeIndexedPath(value string) string {
	path := filepath.ToSlash(strings.TrimSpace(value))
	path = strings.TrimPrefix(path, "./")
	path = strings.TrimLeft(path, "/")
	path = strings.TrimRight(path, "/")
	if path == "." {
		return ""
	}
	return path
}

func normalizeKBaseGlob(value string) string {
	pattern := filepath.ToSlash(strings.TrimSpace(value))
	pattern = strings.TrimPrefix(pattern, "./")
	pattern = strings.TrimLeft(pattern, "/")
	return pattern
}

func normalizeKBaseExt(value string) string {
	ext := strings.ToLower(strings.TrimSpace(value))
	if ext == "" {
		return ""
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return ext
}

func relativeToPrefix(path string, prefix string) (string, bool) {
	path = normalizeIndexedPath(path)
	prefix = normalizeIndexedPath(prefix)
	if prefix == "" {
		return path, true
	}
	if path == prefix {
		return filepath.Base(path), true
	}
	if strings.HasPrefix(path, prefix+"/") {
		return strings.TrimPrefix(path, prefix+"/"), true
	}
	return "", false
}
