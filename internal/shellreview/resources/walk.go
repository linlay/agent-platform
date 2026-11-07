package resources

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"agent-platform/internal/shellreview/types"
)

// WalkSkips reports whether a find walk of target never visits protected
// (both canonical): it lies beyond -maxdepth, or a directory on its path,
// from the first level down, meets the prune clause. typed is the root as
// find received it, or "" when unknown, which leaves -path tests unmet. Names
// come from the directory listings, so a spelling that only matches on a
// case-insensitive volume cannot claim a match find would not make.
func WalkSkips(w types.Walk, typed, target, protected string) bool {
	rel, err := filepath.Rel(target, protected)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if w.MaxDepth >= 0 && len(parts) > w.MaxDepth {
		return true
	}
	dir := target
	for k, name := range parts {
		if !hasEntry(dir, name) {
			return false
		}
		node := filepath.Join(dir, name)
		isDir := k < len(parts)-1
		if !isDir {
			info, err := os.Lstat(node)
			isDir = err == nil && info.IsDir()
		}
		tail := strings.Join(parts[:k+1], "/")
		for _, alt := range w.Prune {
			if pruneMatches(alt, name, tail, typed, isDir) {
				return true
			}
		}
		dir = node
	}
	return false
}

func hasEntry(dir, name string) bool {
	f, err := os.Open(dir)
	if err != nil {
		return false
	}
	defer f.Close()
	names, err := f.Readdirnames(4096)
	return err == nil && slices.Contains(names, name)
}

func pruneMatches(tests []types.WalkTest, name, tail, typed string, isDir bool) bool {
	for _, t := range tests {
		switch t.Primary {
		case "-type":
			if !isDir {
				return false
			}
		case "-name":
			if !findMatch(t.Value, name) {
				return false
			}
		case "-path":
			// Implementations differ on a root typed with a trailing slash
			// (root/x or root//x), so both spellings must match.
			if typed == "" {
				return false
			}
			paths := []string{strings.TrimSuffix(typed, "/") + "/" + tail}
			if strings.HasSuffix(typed, "/") {
				paths = append(paths, typed+"/"+tail)
			}
			for _, p := range paths {
				if !findMatch(t.Value, p) {
					return false
				}
			}
		default:
			return false
		}
	}
	return true
}

// findMatch is fnmatch without flags, as -name and -path use it: * and ?
// also match '/' and a leading dot. Bracket expressions are not modeled, and
// ? is not trusted on non-ASCII names whose byte and character counts
// differ, so a skip is only claimed for patterns read exactly.
func findMatch(pattern, s string) bool {
	if strings.Contains(pattern, "[") || strings.Count(pattern, "*") > 8 {
		return false
	}
	if strings.Contains(pattern, "?") && strings.ContainsFunc(s, func(r rune) bool { return r > 0x7f }) {
		return false
	}
	var match func(i, j int) bool
	match = func(i, j int) bool {
		for i < len(pattern) {
			switch c := pattern[i]; c {
			case '*':
				for k := j; k <= len(s); k++ {
					if match(i+1, k) {
						return true
					}
				}
				return false
			case '?':
				if j >= len(s) {
					return false
				}
			default:
				if c == '\\' {
					if i++; i >= len(pattern) {
						return false
					}
					c = pattern[i]
				}
				if j >= len(s) || s[j] != c {
					return false
				}
			}
			i++
			j++
		}
		return j == len(s)
	}
	return match(0, 0)
}
