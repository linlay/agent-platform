package kbase

import (
	"path/filepath"
	"strings"
)

type pathScope struct {
	prefix   string
	matchers []matcher
	ext      string
}

func newPathScope(prefix string, glob string, typ string) pathScope {
	glob = normalizeKBaseGlob(glob)
	var matchers []matcher
	if glob != "" {
		matchers = compileMatchers([]string{glob})
	}
	return pathScope{
		prefix:   normalizeIndexedPath(prefix),
		matchers: matchers,
		ext:      normalizeKBaseExt(typ),
	}
}

func (s pathScope) active() bool {
	return s.prefix != "" || len(s.matchers) > 0 || s.ext != ""
}

func (s pathScope) matches(path string) bool {
	path = normalizeIndexedPath(path)
	if !pathMatchesPrefix(path, s.prefix) {
		return false
	}
	if len(s.matchers) > 0 && !matchesAny(s.matchers, path) {
		return false
	}
	if s.ext != "" && strings.ToLower(filepath.Ext(path)) != s.ext {
		return false
	}
	return true
}

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

func pathMatchesPrefix(path string, prefix string) bool {
	if prefix == "" {
		return true
	}
	return path == prefix || strings.HasPrefix(path, prefix+"/")
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
