package chat

import (
	"path/filepath"
	"strings"
)

func IsToolInternalPath(path string) bool {
	clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
	if clean == "." || clean == "" {
		return false
	}
	for _, part := range strings.Split(clean, "/") {
		if part == ToolRootDirName {
			return true
		}
	}
	return false
}

func IsToolResultsPath(path string) bool {
	return IsToolInternalPath(path)
}

func IsToolResultRelativePath(path string) bool {
	clean := filepath.Clean(strings.TrimSpace(path))
	if clean == "." || clean == "" || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		return false
	}
	parts := strings.Split(filepath.ToSlash(clean), "/")
	if len(parts) != 3 || parts[0] != ToolRootDirName || parts[1] != ToolResultsDirName {
		return false
	}
	name := strings.TrimSpace(parts[len(parts)-1])
	return name != "" && !strings.Contains(name, "/") && strings.HasSuffix(name, ".json")
}
